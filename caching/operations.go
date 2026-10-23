package caching

import (
	"encoding/base64"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// 操作集的请求与响应（spec/provider/caching-distributed.md §1–2）。

// GetRequest 是 cache-entries.get 的请求。
type GetRequest struct {
	Key string `json:"key"`
}

// GetResponse 是 cache-entries.get 的响应：值的 base64，不存在为 null。
type GetResponse struct {
	Value *string `json:"value"`
}

// SetRequest 是 cache-entries.set 与 cache-entries.try-add 的请求；ttlMs 与 slidingMs 用指针区分"没给"与"给了 0"。
type SetRequest struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	TTLMs     *int64 `json:"ttlMs,omitempty"`
	SlidingMs *int64 `json:"slidingMs,omitempty"`
}

// Empty 是没有字段的响应。
type Empty struct{}

// AddedResponse 是 cache-entries.try-add 的响应。
type AddedResponse struct {
	Added bool `json:"added"`
}

// RemoveRequest 是 cache-entries.remove 的请求。
type RemoveRequest struct {
	Key string `json:"key"`
}

// RemovedResponse 是 cache-entries.remove 与 cache-entries.try-remove 的响应。
type RemovedResponse struct {
	Removed bool `json:"removed"`
}

// TryRemoveRequest 是 cache-entries.try-remove 的请求。
type TryRemoveRequest struct {
	Key      string `json:"key"`
	Expected string `json:"expected"`
}

// RegisterOperations 把 Store 暴露成操作集：五个 internal、不对外发布的操作。
func RegisterOperations(registry *operation.Registry, store Store) error {
	adapter := &operations{store: store}
	if err := operation.Add(registry, contract(OperationGet), operation.HandlerFunc[GetRequest, GetResponse](adapter.get), nil, ""); err != nil {
		return err
	}
	if err := operation.Add(registry, contract(OperationSet), operation.HandlerFunc[SetRequest, Empty](adapter.set), nil, ""); err != nil {
		return err
	}
	if err := operation.Add(registry, contract(OperationTryAdd), operation.HandlerFunc[SetRequest, AddedResponse](adapter.tryAdd), nil, ""); err != nil {
		return err
	}
	if err := operation.Add(registry, contract(OperationRemove), operation.HandlerFunc[RemoveRequest, RemovedResponse](adapter.remove), nil, ""); err != nil {
		return err
	}
	return operation.Add(registry, contract(OperationTryRemove), operation.HandlerFunc[TryRemoveRequest, RemovedResponse](adapter.tryRemove), nil, "")
}

// contract 创建操作集的契约：internal、不暴露。
func contract(id string) operation.Contract {
	return operation.Contract{ID: id, Stability: primitives.Internal}
}

// operations 把操作集的 JSON 形态翻译成 Store 调用。
type operations struct {
	store Store
}

// get 实现 cache-entries.get。
func (o *operations) get(ctx *invocation.Context, request GetRequest) primitives.Result[GetResponse] {
	value, found, err := o.store.Get(ctx, request.Key)
	if err != nil {
		return primitives.Fail[GetResponse](storeError(err))
	}
	if !found {
		return primitives.Ok(GetResponse{})
	}
	encoded := base64.StdEncoding.EncodeToString(value)
	return primitives.Ok(GetResponse{Value: &encoded})
}

// set 实现 cache-entries.set。
func (o *operations) set(ctx *invocation.Context, request SetRequest) primitives.Result[Empty] {
	value, options, err := decodeSet(request)
	if err != nil {
		return primitives.Fail[Empty](err)
	}
	if err := o.store.Set(ctx, request.Key, value, options); err != nil {
		return primitives.Fail[Empty](storeError(err))
	}
	return primitives.Ok(Empty{})
}

// tryAdd 实现 cache-entries.try-add。
func (o *operations) tryAdd(ctx *invocation.Context, request SetRequest) primitives.Result[AddedResponse] {
	value, options, err := decodeSet(request)
	if err != nil {
		return primitives.Fail[AddedResponse](err)
	}
	added, storeErr := o.store.TryAdd(ctx, request.Key, value, options)
	if storeErr != nil {
		return primitives.Fail[AddedResponse](storeError(storeErr))
	}
	return primitives.Ok(AddedResponse{Added: added})
}

// remove 实现 cache-entries.remove。
func (o *operations) remove(ctx *invocation.Context, request RemoveRequest) primitives.Result[RemovedResponse] {
	removed, err := o.store.Remove(ctx, request.Key)
	if err != nil {
		return primitives.Fail[RemovedResponse](storeError(err))
	}
	return primitives.Ok(RemovedResponse{Removed: removed})
}

// tryRemove 实现 cache-entries.try-remove。
func (o *operations) tryRemove(ctx *invocation.Context, request TryRemoveRequest) primitives.Result[RemovedResponse] {
	expected, err := decodeValue(request.Expected)
	if err != nil {
		return primitives.Fail[RemovedResponse](err)
	}
	removed, storeErr := o.store.TryRemove(ctx, request.Key, expected)
	if storeErr != nil {
		return primitives.Fail[RemovedResponse](storeError(storeErr))
	}
	return primitives.Ok(RemovedResponse{Removed: removed})
}

// decodeSet 解码写入请求：键与选项按子系统规则校验（插件侧也必须校验），值是 base64。
func decodeSet(request SetRequest) ([]byte, Options, *primitives.Error) {
	if err := ValidateKey(request.Key); err != nil {
		return nil, Options{}, err
	}
	var options Options
	if request.TTLMs != nil {
		if *request.TTLMs <= 0 {
			return nil, Options{}, primitives.Validation(ErrorCodeInvalidOptions, MessageInvalidOptions, map[string]any{"reason": "ttlMs must be a positive integer"})
		}
		options.TimeToLive = time.Duration(*request.TTLMs) * time.Millisecond
	}
	if request.SlidingMs != nil {
		if *request.SlidingMs <= 0 {
			return nil, Options{}, primitives.Validation(ErrorCodeInvalidOptions, MessageInvalidOptions, map[string]any{"reason": "slidingMs must be a positive integer"})
		}
		options.Sliding = time.Duration(*request.SlidingMs) * time.Millisecond
	}
	if err := options.Validate(); err != nil {
		return nil, Options{}, err
	}
	value, err := decodeValue(request.Value)
	if err != nil {
		return nil, Options{}, err
	}
	return value, options, nil
}

// decodeValue 解码 base64 的值；不合法按载荷不合法。
func decodeValue(encoded string) ([]byte, *primitives.Error) {
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, primitives.Validation(operation.ErrorCodeInvalidPayload, operation.MessageInvalidPayload, map[string]any{"reason": "the value is not standard base64"})
	}
	return value, nil
}

// storeError 把 Store 的错误转成原语层错误：*primitives.Error 原样，其他按后端不可用。
func storeError(err error) *primitives.Error {
	if typed, ok := err.(*primitives.Error); ok {
		return typed
	}
	return Unavailable(err.Error())
}
