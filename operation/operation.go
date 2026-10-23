// Package operation 是操作契约与 XGI 消息（RFC-0011）：业务以操作为单位声明，调用器让它经过完整管道并在 handle 阶段执行；
// 请求与响应用 encoding/json 编解码，JSON 是 XGI 的规范编码。
package operation

import (
	"bytes"
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

// 上下文对象袋里的键。
type contextKey int

const (
	descriptorKey contextKey = iota
	requestKey
	responseKey
)

// Current 返回当前调用的操作描述（管道里的中间件用）。
func Current(ctx *invocation.Context) (*Descriptor, bool) {
	value, ok := ctx.Get(descriptorKey)
	if !ok {
		return nil, false
	}
	return value.(*Descriptor), true
}

// RequestOf 返回当前调用的 XGI 请求。
func RequestOf(ctx *invocation.Context) (Request, bool) {
	value, ok := ctx.Get(requestKey)
	if !ok {
		return Request{}, false
	}
	return value.(Request), true
}

// ResponsePayloadOf 返回 handle 阶段写回的响应载荷（成功时）。
func ResponsePayloadOf(ctx *invocation.Context) ([]byte, bool) {
	value, ok := ctx.Get(responseKey)
	if !ok {
		return nil, false
	}
	return value.([]byte), true
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
	ctx.Set(descriptorKey, descriptor)
	ctx.Set(requestKey, request)
	outcome := i.step(ctx)
	if !outcome.OK() {
		return Response{Outcome: outcome}
	}
	payload, _ := ResponsePayloadOf(ctx)
	return Response{Outcome: outcome, Payload: payload}
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
	ctx.Set(descriptorKey, descriptor)
	ctx.Set(requestKey, Request{ID: id})
	outcome := invoker.stepWith(ctx, func(ctx *invocation.Context) primitives.Result[primitives.Unit] {
		result = handler.Handle(ctx, request)
		return result.Unit()
	})
	if !outcome.OK() {
		return primitives.Fail[Res](outcome.Err())
	}
	return result
}

// stepWith 以自定义终点执行管道：类型化调用在 handle 阶段直接调处理器。
func (i *Invoker) stepWith(ctx *invocation.Context, terminal pipeline.Step) primitives.Result[primitives.Unit] {
	ctx.Set(terminalKey, terminal)
	return i.step(ctx)
}

// terminalKey 是类型化调用放在对象袋里的终点。
const terminalKey contextKey = 100

// dispatch 是管道的终点：执行当前操作并把响应载荷写回上下文。
func dispatch(ctx *invocation.Context) primitives.Result[primitives.Unit] {
	if terminal, ok := ctx.Get(terminalKey); ok {
		return terminal.(pipeline.Step)(ctx)
	}
	descriptor, ok := Current(ctx)
	if !ok {
		return primitives.Fail[primitives.Unit](primitives.NotFound(ErrorCodeNotFound, MessageNotFound, nil))
	}
	request, _ := RequestOf(ctx)
	response := descriptor.Execute(ctx, request.Payload)
	if response.Outcome.OK() {
		ctx.Set(responseKey, response.Payload)
	}
	return response.Outcome
}
