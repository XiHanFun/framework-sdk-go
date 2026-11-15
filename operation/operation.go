// Package operation 是操作契约与 XGI 消息（RFC-0011）：业务以操作为单位声明，调用器让它经过完整管道并在 handle 阶段执行；
// 请求与响应用 encoding/json 编解码，JSON 是 XGI 的规范编码。
package operation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"

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
)

// 操作错误的消息键。
const (
	MessageNotFound       = "kernel.operation.unknown"
	MessageInvalidPayload = "kernel.operation.payload_invalid"
	MessageTypeMismatch   = "kernel.operation.type_mismatch"
)

// Capacity 是登记表的容量。
const Capacity = 10000

// 暴露的协议名。
const (
	ExposeHTTP        = "http"
	ExposeGrpc        = "grpc"
	ExposeMcp         = "mcp"
	ExposeCommandLine = "commandline"
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
	execute func(ctx *invocation.Context, payload []byte) Response
}

// Execute 解码载荷、执行、编码响应；解码失败返回 KERNEL_VALIDATION_001。
func (d *Descriptor) Execute(ctx *invocation.Context, payload []byte) Response {
	return d.execute(ctx, payload)
}

// Registry 是操作登记表：ID 唯一，容量 Capacity。
type Registry struct {
	mu          sync.RWMutex
	descriptors map[string]*Descriptor
}

// NewRegistry 创建空的登记表。
func NewRegistry() *Registry { return &Registry{descriptors: map[string]*Descriptor{}} }

// Add 登记一个操作；ID 不合法是 PRIMITIVES_VALIDATION_001，重复是 KERNEL_OPERATION_001，满了是 KERNEL_OPERATION_002。
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
	descriptor := &Descriptor{Contract: contract, Schema: schema, Owner: owner, handler: handler}
	descriptor.execute = func(ctx *invocation.Context, payload []byte) Response {
		request, err := decode[Req](payload)
		if err != nil {
			return Response{Outcome: primitives.Fail[primitives.Unit](err)}
		}
		result := handler.Handle(ctx, request)
		value, ok := result.Value()
		if !ok {
			return Response{Outcome: primitives.Fail[primitives.Unit](result.Err())}
		}
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return Response{Outcome: primitives.Fail[primitives.Unit](primitives.Literal("KERNEL_UNEXPECTED_001", "The response of '"+contract.ID+"' could not be encoded: "+marshalErr.Error(), nil, nil))}
		}
		return Response{Outcome: primitives.Success(), Payload: encoded}
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.descriptors[contract.ID]; exists {
		return &primitives.FrameworkError{Code: ErrorCodeDuplicate, Message: fmt.Sprintf("The operation '%s' is already registered.", contract.ID)}
	}
	if len(registry.descriptors) >= Capacity {
		return &primitives.FrameworkError{Code: ErrorCodeRegistryFull, Message: fmt.Sprintf("The operation registry is full (%d operations).", Capacity)}
	}
	registry.descriptors[contract.ID] = descriptor
	return nil
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

// decode 解码请求载荷：空等价于 {}；非 JSON、null 或类型不匹配为 KERNEL_VALIDATION_001。
func decode[Req any](payload []byte) (Req, *primitives.Error) {
	var request Req
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return request, primitives.Validation(ErrorCodeInvalidPayload, MessageInvalidPayload, map[string]any{"reason": "the payload is null"})
	}
	if err := json.Unmarshal(trimmed, &request); err != nil {
		return request, primitives.Validation(ErrorCodeInvalidPayload, MessageInvalidPayload, map[string]any{"reason": err.Error()})
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

// Invoker 让操作经过完整管道执行。
type Invoker struct {
	registry *Registry
	step     pipeline.Step
}

// NewInvoker 用登记表与管道构建调用器：管道的终点在 handle 阶段分派当前操作。
func NewInvoker(registry *Registry, builder *pipeline.Builder) (*Invoker, error) {
	if builder == nil {
		builder = pipeline.NewBuilder()
	}
	step, err := builder.Build(dispatch)
	if err != nil {
		return nil, err
	}
	return &Invoker{registry: registry, step: step}, nil
}

// Registry 返回登记表。
func (i *Invoker) Registry() *Registry { return i.registry }

// Invoke 执行一个 XGI 请求：未登记返回 KERNEL_NOTFOUND_001（不进管道）；否则经完整管道执行。
func (i *Invoker) Invoke(ctx *invocation.Context, request Request) Response {
	descriptor, ok := i.registry.Get(request.ID)
	if !ok {
		return Response{Outcome: primitives.Fail[primitives.Unit](primitives.NotFound(ErrorCodeNotFound, MessageNotFound, map[string]any{"id": request.ID}))}
	}
	state := &call{descriptor: descriptor, request: request}
	state.terminal = func(ctx *invocation.Context) primitives.Result[primitives.Unit] {
		response := descriptor.Execute(ctx, request.Payload)
		if response.Outcome.OK() {
			state.response, state.responded = response.Payload, true
		}
		return response.Outcome
	}
	outcome := i.step(begin(ctx, state))
	if !outcome.OK() {
		return Response{Outcome: outcome}
	}
	return Response{Outcome: outcome, Payload: state.response}
}

// Invoke 是类型化入口：不经 JSON，直接以请求对象执行；类型与登记不符返回 KERNEL_VALIDATION_002。
func Invoke[Req, Res any](invoker *Invoker, ctx *invocation.Context, id string, request Req) primitives.Result[Res] {
	descriptor, ok := invoker.registry.Get(id)
	if !ok {
		return primitives.Fail[Res](primitives.NotFound(ErrorCodeNotFound, MessageNotFound, map[string]any{"id": id}))
	}
	handler, ok := descriptor.handler.(Handler[Req, Res])
	if !ok {
		return primitives.Fail[Res](primitives.Validation(ErrorCodeTypeMismatch, MessageTypeMismatch, map[string]any{"id": id}))
	}
	var result primitives.Result[Res]
	state := &call{descriptor: descriptor, request: Request{ID: id}}
	state.terminal = func(ctx *invocation.Context) primitives.Result[primitives.Unit] {
		result = handler.Handle(ctx, request)
		return result.Unit()
	}
	outcome := invoker.step(begin(ctx, state))
	if !outcome.OK() {
		return primitives.Fail[Res](outcome.Err())
	}
	return result
}

// dispatch 是管道的终点：执行这次调用自己的终点（XGI 调用解码、执行、编码，类型化调用直接调处理器）
func dispatch(ctx *invocation.Context) primitives.Result[primitives.Unit] {
	state, ok := current(ctx)
	if !ok {
		return primitives.Fail[primitives.Unit](primitives.NotFound(ErrorCodeNotFound, MessageNotFound, nil))
	}
	return state.terminal(ctx)
}
