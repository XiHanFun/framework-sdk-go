// Package operation 是操作契约与 XGI 消息（RFC-0011）：业务以操作为单位声明，调用器让它经过完整管道并在 handle 阶段执行；
// 请求与响应用 encoding/json 编解码，JSON 是 XGI 的规范编码。
package operation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/xihanfun/framework-sdk-go/internal/jsoncodec"
	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// 内核登记的操作错误码。
const (
	// ErrorCodeNotFound：操作未登记。
	ErrorCodeNotFound = "KERNEL_NOTFOUND_001"
	// ErrorCodeInvalidPayload：操作载荷不合法（非 JSON、null 或类型不匹配）。
	ErrorCodeInvalidPayload = "KERNEL_VALIDATION_001"
	// ErrorCodeTypeMismatch：类型化调用的请求或响应类型与登记不符。
	ErrorCodeTypeMismatch = "KERNEL_VALIDATION_002"
	// ErrorCodeDuplicate：操作 ID 重复登记（配置期）。
	ErrorCodeDuplicate = "KERNEL_OPERATION_001"
	// ErrorCodeRegistryFull：操作登记表已满（配置期）。
	ErrorCodeRegistryFull = "KERNEL_OPERATION_002"
	// ErrorCodeUnexpected：调用以成功结束却没有产出响应，或响应无法编码
	ErrorCodeUnexpected = "KERNEL_UNEXPECTED_001"
)

// 操作错误的消息键与参数，与 .NET KernelMessages 一致
const (
	// MessageNotFound：操作未登记，参数 {id}
	MessageNotFound = "kernel.operation.unknown"
	// MessageInvalidPayload：载荷不合法，参数 {id, reason}；reason 只写出错位置的 JSON 路径（RFC-0011 规则 2）
	MessageInvalidPayload = "kernel.operation.invalid_payload"
	// MessageTypeMismatch：类型化调用的类型与登记不符，参数 {id, expected, actual}，值是「请求类型 -> 响应类型」
	MessageTypeMismatch = "kernel.operation.type_mismatch"
	// MessageNoResponse：中间件以成功短路、处理器没有产出响应，参数 {id}
	MessageNoResponse = "kernel.operation.no_response"
	// MessageDuplicate：重复登记，参数 {id, owner}，owner 是已登记者的归属
	MessageDuplicate = "kernel.operation.duplicate"
	// MessageRegistryFull：登记表已满，参数 {capacity}
	MessageRegistryFull = "kernel.operation.registry_full"
)

// 这些键的 en-US 默认模板，逐字照 .NET XiHan.Framework.Kernel/Localization/en-US.json
func init() {
	primitives.RegisterDefaultTemplates(map[string]string{
		MessageNotFound:       "Operation '{id}' is not registered.",
		MessageInvalidPayload: "The payload of operation '{id}' is not valid: {reason}",
		MessageTypeMismatch:   "Operation '{id}' handles '{expected}', but '{actual}' was supplied.",
		MessageNoResponse:     "Operation '{id}' completed without producing a response.",
		MessageDuplicate:      "Operation '{id}' is already registered by '{owner}'.",
		MessageRegistryFull:   "The operation registry has reached its capacity of {capacity}.",
	})
}

// InvalidPayload 创建载荷不合法的错误（KERNEL_VALIDATION_001）：reason 是出错位置的 JSON 路径（如 $.quantity、$），
// 不带目标类型名（RFC-0011 规则 2）；载荷是 null、不是对象或根上就不合时 reason 是 $
func InvalidPayload(id, reason string) *primitives.Error {
	return primitives.Validation(ErrorCodeInvalidPayload, MessageInvalidPayload, map[string]any{"id": id, "reason": reason})
}

// Capacity 是登记表的容量。
const Capacity = 10000

// 暴露的协议名，与 plugin.proto 的 OperationDeclaration.expose 一致
const (
	ExposeHTTP        = "http"
	ExposeGrpc        = "grpc"
	ExposeMcp         = "mcp"
	ExposeCommandLine = "commandline"
	// ExposeAI 是 AI 工具调用（RFC-0022）
	ExposeAI = "ai"
)

// idPattern 是操作 ID 文法 <resource>.<verb>。
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*\.[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// IsID 判断操作 ID 是否符合 <resource>.<verb>。
func IsID(id string) bool { return idPattern.MatchString(id) }

// Contract 是操作契约：ID、稳定性、暴露的协议与授权要求（permission 与 policy 二选一）。
type Contract struct {
	ID         string
	Stability  primitives.Stability
	Expose     []string
	Permission string
	Policy     string
}

// Schema 是请求与响应的 JSON Schema（draft 2020-12）文本。
type Schema struct {
	Request  string
	Response string
}

// Request 是 XGI 请求：操作 ID 与请求类型的 UTF-8 JSON（空等价于 {}）。
type Request struct {
	ID      string
	Payload []byte
}

// Response 是 XGI 响应：结果与成功时响应类型的 UTF-8 JSON。
type Response struct {
	Outcome primitives.Result[primitives.Unit]
	Payload []byte
}

// Handler 处理一个操作：可恢复失败走 Result，不 panic。
type Handler[Req, Res any] interface {
	Handle(ctx *invocation.Context, request Req) primitives.Result[Res]
}

// HandlerFunc 让函数成为 Handler。
type HandlerFunc[Req, Res any] func(ctx *invocation.Context, request Req) primitives.Result[Res]

// Handle 实现 Handler。
func (f HandlerFunc[Req, Res]) Handle(ctx *invocation.Context, request Req) primitives.Result[Res] {
	return f(ctx, request)
}

// Descriptor 是登记表里的一个操作：契约、Schema、归属与不经反射的执行入口。
type Descriptor struct {
	// Contract 是契约。
	Contract Contract
	// Schema 是可选的 Schema。
	Schema *Schema
	// Owner 是归属（清单 ID 或 app）。
	Owner   string
	handler any
	types   string
	// decode 把载荷解码成请求（Validate 阶段的内置步骤用）；handle 执行处理器并编码响应
	decode func(payload []byte) (any, *primitives.Error)
	handle func(ctx *invocation.Context, request any) Response
}

// Execute 解码载荷、执行、编码响应；解码失败返回 KERNEL_VALIDATION_001。不经管道
func (d *Descriptor) Execute(ctx *invocation.Context, payload []byte) Response {
	request, err := d.decode(payload)
	if err != nil {
		return Response{Outcome: primitives.Fail[primitives.Unit](err)}
	}
	return d.handle(ctx, request)
}

// Registry 是操作登记表：ID 唯一，容量 Capacity。
type Registry struct {
	mu          sync.RWMutex
	descriptors map[string]*Descriptor
}

// NewRegistry 创建空的登记表。
func NewRegistry() *Registry { return &Registry{descriptors: map[string]*Descriptor{}} }

// Add 登记一个操作。ID 不合法、permission 与 policy 同时给出是编程错误，返回 FrameworkError（PRIMITIVES_VALIDATION_001）；
// 重复登记与登记表已满是登记失败，返回 *primitives.Error：KERNEL_OPERATION_001（kernel.operation.duplicate，参数 {id, owner}）
// 与 KERNEL_OPERATION_002（kernel.operation.registry_full，参数 {capacity}），与 .NET OperationRegistry 一致
func Add[Req, Res any](registry *Registry, contract Contract, handler Handler[Req, Res], schema *Schema, owner string) error {
	if !IsID(contract.ID) {
		return &primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: fmt.Sprintf("The operation ID '%s' must match <resource>.<verb>.", contract.ID)}
	}
	if contract.Permission != "" && contract.Policy != "" {
		return &primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: fmt.Sprintf("The operation '%s' must declare either a permission or a policy.", contract.ID)}
	}
	if owner == "" {
		owner = pipeline.AppOwner
	}
	descriptor := &Descriptor{Contract: contract, Schema: schema, Owner: owner, handler: handler, types: signature[Req, Res]()}
	descriptor.decode = func(payload []byte) (any, *primitives.Error) {
		request, err := decode[Req](contract.ID, payload)
		if err != nil {
			return nil, err
		}
		return request, nil
	}
	descriptor.handle = func(ctx *invocation.Context, request any) Response {
		result := handler.Handle(ctx, request.(Req))
		value, ok := result.Value()
		if !ok {
			return Response{Outcome: primitives.Fail[primitives.Unit](result.Err())}
		}
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return Response{Outcome: primitives.Fail[primitives.Unit](primitives.Literal(ErrorCodeUnexpected, "The response of '"+contract.ID+"' could not be encoded: "+marshalErr.Error(), nil, nil))}
		}
		return Response{Outcome: primitives.Success(), Payload: encoded}
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing, exists := registry.descriptors[contract.ID]; exists {
		return primitives.NewError(ErrorCodeDuplicate, MessageDuplicate, map[string]any{"id": contract.ID, "owner": existing.Owner}, nil)
	}
	if len(registry.descriptors) >= Capacity {
		return primitives.NewError(ErrorCodeRegistryFull, MessageRegistryFull, map[string]any{"capacity": Capacity}, nil)
	}
	registry.descriptors[contract.ID] = descriptor
	return nil
}

// signature 返回「请求类型 -> 响应类型」，类型化调用不符时写进 expected / actual（与 .NET 一样用类型的短名）
func signature[Req, Res any]() string {
	return typeName(reflect.TypeFor[Req]()) + " -> " + typeName(reflect.TypeFor[Res]())
}

// typeName 返回类型的短名；没有名字的类型（切片、映射、匿名结构体、指针）用它的文本形式
func typeName(t reflect.Type) string {
	if t.Name() != "" {
		return t.Name()
	}
	return t.String()
}

// Get 按 ID 取描述。
func (r *Registry) Get(id string) (*Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptor, ok := r.descriptors[id]
	return descriptor, ok
}

// List 按 ID 升序列出全部描述。
func (r *Registry) List() []*Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptors := make([]*Descriptor, 0, len(r.descriptors))
	for _, descriptor := range r.descriptors {
		descriptors = append(descriptors, descriptor)
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Contract.ID < descriptors[j].Contract.ID })
	return descriptors
}

// decode 解码请求载荷（RFC-0011 规则 2，ADR-0236）：空载荷等价于 {}；先过共同的 JSON 读取检查，语法错误（含只有空白、BOM、嵌套超过 64 层、
// 孤立的代理项）的 reason 是 $，任何一层的重复成员写第一处重复的路径；载荷是 null 时 reason 是 $；再按请求类型解码，按文本顺序报第一处
// 类型不符的路径，带 xihan:"required" 的成员缺少时在所在对象结束时报该对象的路径。都是 KERNEL_VALIDATION_001
func decode[Req any](id string, payload []byte) (Req, *primitives.Error) {
	var request Req
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	root, problem := jsontext.Parse(payload)
	if problem != nil {
		return request, InvalidPayload(id, problem.Path)
	}
	// JSON null 不是请求对象：载荷不合法，reason 写 $（ADR-0235，旧版写 the payload is null）
	if root.Kind == jsontext.Null {
		return request, InvalidPayload(id, "$")
	}
	if problem := jsoncodec.Decode(payload, root, &request); problem != nil {
		return *new(Req), InvalidPayload(id, problem.Path)
	}
	return request, nil
}

// callKey 是一次调用的状态在派生上下文里的键
type callKey struct{}

// call 是一次调用的状态：描述、请求、handle 阶段的终点与它写回的响应载荷。每次调用各有一份，经派生的上下文传给管道，
// 不写进对象袋：同一上下文上先后、嵌套或并发的调用互不串用，调用结束后调用方的上下文与对象袋保持原样
type call struct {
	descriptor *Descriptor
	request    Request
	terminal   pipeline.Step
	response   []byte
	responded  bool
	// raw 为真表示原始调用：Validate 阶段的内置步骤把载荷解码进 decoded；类型化调用跳过解码
	raw     bool
	decoded any
	ready   bool
}

// begin 为一次调用派生上下文：管道与处理器看到的是它，调用方的上下文不变
func begin(ctx *invocation.Context, state *call) *invocation.Context {
	scoped := *ctx
	scoped.Context = context.WithValue(ctx.Context, callKey{}, state)
	return &scoped
}

// current 取当前调用的状态
func current(ctx *invocation.Context) (*call, bool) {
	if ctx == nil || ctx.Context == nil {
		return nil, false
	}
	state, ok := ctx.Value(callKey{}).(*call)
	return state, ok
}

// Current 返回当前调用的操作描述（管道里的中间件用）；只在调用期间、经管道传下来的上下文上可见
func Current(ctx *invocation.Context) (*Descriptor, bool) {
	state, ok := current(ctx)
	if !ok {
		return nil, false
	}
	return state.descriptor, true
}

// RequestOf 返回当前调用的 XGI 请求；类型化调用只有 ID
func RequestOf(ctx *invocation.Context) (Request, bool) {
	state, ok := current(ctx)
	if !ok {
		return Request{}, false
	}
	return state.request, true
}

// ResponsePayloadOf 返回 handle 阶段写回的响应载荷（成功时）；还没有响应时第二个返回值为假
func ResponsePayloadOf(ctx *invocation.Context) ([]byte, bool) {
	state, ok := current(ctx)
	if !ok || !state.responded {
		return nil, false
	}
	return state.response, true
}

// Invoker 让操作经过完整管道执行：进入管道前只核对未登记（KERNEL_NOTFOUND_001）与类型化调用的类型不符（KERNEL_VALIDATION_002），
// 按这个顺序；截止时间、解码与异常翻译是管道里的内置步骤（RFC-0011 规则 10）
type Invoker struct {
	registry *Registry
	step     pipeline.Step
	pipeline *pipeline.Builder
}

// 调用器的三个内置步骤（RFC-0011 规则 10，ADR-0236）：名字与 .NET 的内置中间件相同，都按归属 xihan.kernel.core 放在所在阶段的
// Beginning，与同位置的其他中间件按 RFC-0004 的规则排序（归属的序数序、同归属按登记顺序）
const (
	// ObserveMiddleware 在 Observe 阶段翻译异常：排在它之后的中间件与处理器的 panic 翻译为 KERNEL_UNEXPECTED_001
	ObserveMiddleware = "ObserveMiddleware"
	// DeadlineMiddleware 在 Resilience 阶段核对截止时间：进入时已到即 KERNEL_TIMEOUT_001（deadline_before_start），并承担途中的归因
	DeadlineMiddleware = "DeadlineMiddleware"
	// ValidationMiddleware 在 Validate 阶段解码：原始调用在这里把载荷解码成请求，类型化调用跳过
	ValidationMiddleware = "ValidationMiddleware"
)

// NewInvoker 用登记表与管道构建调用器：在管道的副本里加上三个内置步骤（builder 本身不变），终点在 handle 阶段之后分派当前操作。
// builder 里已有同名的中间件时 panic（RFC-0004 规则 8）
func NewInvoker(registry *Registry, builder *pipeline.Builder) (*Invoker, error) {
	if builder == nil {
		builder = pipeline.NewBuilder()
	}
	composed := builder.Clone().
		UseAt(pipeline.Func(ObserveMiddleware, observe), primitives.Observe, pipeline.Beginning, pipeline.KernelOwner).
		UseAt(pipeline.Func(DeadlineMiddleware, deadline), primitives.Resilience, pipeline.Beginning, pipeline.KernelOwner).
		UseAt(pipeline.Func(ValidationMiddleware, validation), primitives.Validate, pipeline.Beginning, pipeline.KernelOwner)
	step, err := composed.Build(dispatch)
	if err != nil {
		return nil, err
	}
	return &Invoker{registry: registry, step: step, pipeline: composed}, nil
}

// Pipeline 返回调用器实际执行的管道（含三个内置步骤）的描述，供 xihan pipeline dump 这类诊断
func (i *Invoker) Pipeline() ([]pipeline.Descriptor, error) { return i.pipeline.Describe() }

// Registry 返回登记表。
func (i *Invoker) Registry() *Registry { return i.registry }

// Invoke 执行一个 XGI 请求：未登记返回 KERNEL_NOTFOUND_001（不进管道）；否则经完整管道执行，载荷在 Validate 阶段解码
func (i *Invoker) Invoke(ctx *invocation.Context, request Request) Response {
	descriptor, ok := i.registry.Get(request.ID)
	if !ok {
		return Response{Outcome: primitives.Fail[primitives.Unit](primitives.NotFound(ErrorCodeNotFound, MessageNotFound, map[string]any{"id": request.ID}))}
	}
	state := &call{descriptor: descriptor, request: request, raw: true}
	state.terminal = func(ctx *invocation.Context) primitives.Result[primitives.Unit] {
		if err := state.decodeOnce(); err != nil {
			return primitives.Fail[primitives.Unit](err)
		}
		response := descriptor.handle(ctx, state.decoded)
		if response.Outcome.OK() {
			state.response, state.responded = response.Payload, true
		}
		return response.Outcome
	}
	outcome := i.step(begin(ctx, state))
	if !outcome.OK() {
		return Response{Outcome: outcome}
	}
	if !state.responded {
		return Response{Outcome: primitives.Fail[primitives.Unit](noResponse(request.ID))}
	}
	return Response{Outcome: outcome, Payload: state.response}
}

// decodeOnce 解码原始调用的载荷，只解码一次
func (c *call) decodeOnce() *primitives.Error {
	if !c.raw || c.ready {
		return nil
	}
	request, err := c.descriptor.decode(c.request.Payload)
	if err != nil {
		return err
	}
	c.decoded, c.ready = request, true
	return nil
}

// Invoke 是类型化入口：不经 JSON，直接以请求对象执行；类型与登记不符返回 KERNEL_VALIDATION_002，
// 中间件以成功短路、处理器没有产出响应时返回 KERNEL_UNEXPECTED_001（kernel.operation.no_response）
func Invoke[Req, Res any](invoker *Invoker, ctx *invocation.Context, id string, request Req) primitives.Result[Res] {
	descriptor, ok := invoker.registry.Get(id)
	if !ok {
		return primitives.Fail[Res](primitives.NotFound(ErrorCodeNotFound, MessageNotFound, map[string]any{"id": id}))
	}
	handler, ok := descriptor.handler.(Handler[Req, Res])
	if !ok {
		return primitives.Fail[Res](primitives.Validation(ErrorCodeTypeMismatch, MessageTypeMismatch, map[string]any{"id": id, "expected": descriptor.types, "actual": signature[Req, Res]()}))
	}
	var result primitives.Result[Res]
	produced := false
	state := &call{descriptor: descriptor, request: Request{ID: id}}
	state.terminal = func(ctx *invocation.Context) primitives.Result[primitives.Unit] {
		result = handler.Handle(ctx, request)
		produced = result.OK()
		return result.Unit()
	}
	outcome := invoker.step(begin(ctx, state))
	if !outcome.OK() {
		return primitives.Fail[Res](outcome.Err())
	}
	if !produced {
		return primitives.Fail[Res](noResponse(id))
	}
	return result
}

// observe 是 Observe 阶段的内置步骤：把排在它之后的中间件与处理器的 panic 翻译为 KERNEL_UNEXPECTED_001（kernel.invocation.unhandled_exception，
// 没有参数），与 .NET 的 ObserveMiddleware 一致（RFC-0011 规则 5、规则 10）；panic 的值是 FrameworkError 时作为内部错误带上。
// panic 的值与堆栈只写进日志（slog.Default，插件进程里是 stderr），不进错误。排在它之前的（Ingress 阶段，或 Observe 阶段 Beginning 上
// 归属排在 xihan.kernel.core 之前的）panic 原样交给调用方
func observe(ctx *invocation.Context, next pipeline.Step) (outcome primitives.Result[primitives.Unit]) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		id := ""
		if state, ok := current(ctx); ok {
			id = state.descriptor.Contract.ID
		}
		slog.Default().Error("an operation panicked", "operation", id, "traceId", ctx.TraceID(), "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
		var inner *primitives.Error
		var framework *primitives.FrameworkError
		if err, ok := recovered.(error); ok && errors.As(err, &framework) {
			inner = framework.ToError()
		}
		outcome = primitives.Fail[primitives.Unit](primitives.Unexpected(ErrorCodeUnexpected, invocation.MessageUnhandledException, nil, inner))
	}()
	return next(ctx)
}

// deadline 是 Resilience 阶段的内置步骤，承担 .NET DeadlineMiddleware 的判断（RFC-0004、ADR-0092）：进入时截止时间已到即
// KERNEL_TIMEOUT_001（kernel.invocation.deadline_before_start），后面的中间件与处理器不执行；进入时不核对取消。下游写下
// KERNEL_CANCELLED_001 而截止令牌已触发时改写为 KERNEL_TIMEOUT_001（kernel.invocation.deadline_exceeded），不论调用方是否也取消了
func deadline(ctx *invocation.Context, next pipeline.Step) primitives.Result[primitives.Unit] {
	if expiry, ok := ctx.Deadline(); ok && !time.Now().Before(expiry) {
		return primitives.Fail[primitives.Unit](primitives.Timeout(invocation.ErrorCodeDeadlineExceeded, invocation.MessageDeadlineBeforeStart, nil))
	}
	outcome := next(ctx)
	if err := outcome.Err(); err != nil && err.Code == invocation.ErrorCodeCancelled && ctx.DeadlineExceeded() {
		return primitives.Fail[primitives.Unit](primitives.Timeout(invocation.ErrorCodeDeadlineExceeded, invocation.MessageDeadlineExceeded, nil))
	}
	return outcome
}

// validation 是 Validate 阶段的内置步骤：原始调用在这里把载荷解码成请求（RFC-0011 规则 2），失败即以 KERNEL_VALIDATION_001 结束；
// 类型化调用跳过
func validation(ctx *invocation.Context, next pipeline.Step) primitives.Result[primitives.Unit] {
	if state, ok := current(ctx); ok {
		if err := state.decodeOnce(); err != nil {
			return primitives.Fail[primitives.Unit](err)
		}
	}
	return next(ctx)
}

// noResponse 是调用以成功结束、处理器却没有产出响应的错误（中间件以成功短路）
func noResponse(id string) *primitives.Error {
	return primitives.Unexpected(ErrorCodeUnexpected, MessageNoResponse, map[string]any{"id": id}, nil)
}

// dispatch 是管道的终点：执行这次调用自己的终点（XGI 调用解码、执行、编码，类型化调用直接调处理器）
func dispatch(ctx *invocation.Context) primitives.Result[primitives.Unit] {
	state, ok := current(ctx)
	if !ok {
		return primitives.Fail[primitives.Unit](primitives.NotFound(ErrorCodeNotFound, MessageNotFound, nil))
	}
	return state.terminal(ctx)
}
