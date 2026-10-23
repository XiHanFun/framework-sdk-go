package plugin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// ToProtoError 把原语层错误编码为协议的 Error：details 是 JSON 对象文本，inner 递归。
func ToProtoError(err *primitives.Error) *pluginv1.Error {
	if err == nil {
		return nil
	}
	encoded := &pluginv1.Error{Code: err.Code, MessageKey: err.MessageKey, Text: err.Message()}
	if len(err.Details) > 0 {
		if details, marshalErr := json.Marshal(err.Details); marshalErr == nil {
			encoded.Details = string(details)
		}
	}
	if err.Inner != nil {
		encoded.Inner = ToProtoError(err.Inner)
	}
	return encoded
}

// FromProtoError 把协议的 Error 还原为原语层错误；码不合法时换成 PLUGINS_UNEXPECTED_001 并保留原码在细节里。
func FromProtoError(err *pluginv1.Error) *primitives.Error {
	if err == nil {
		return nil
	}
	var details map[string]any
	if err.Details != "" {
		_ = json.Unmarshal([]byte(err.Details), &details)
	}
	code := err.Code
	if !primitives.IsErrorCode(code) {
		if details == nil {
			details = map[string]any{}
		}
		details["code"] = err.Code
		code = ErrorCodeUnexpected
	}
	decoded := &primitives.Error{Code: code, MessageKey: err.MessageKey, Text: err.Text, Details: details}
	if decoded.MessageKey != "" && !primitives.IsMessageKey(decoded.MessageKey) {
		decoded.MessageKey = ""
	}
	if err.Inner != nil {
		decoded.Inner = FromProtoError(err.Inner)
	}
	return decoded
}

// ToStatus 把结果编码为协议的 Status。
func ToStatus(result primitives.Result[primitives.Unit]) *pluginv1.Status {
	if result.OK() {
		return &pluginv1.Status{Ok: true}
	}
	return &pluginv1.Status{Ok: false, Error: ToProtoError(result.Err())}
}

// FromStatus 把协议的 Status 还原为结果；失败但没有错误时为 PLUGINS_UNEXPECTED_001。
func FromStatus(status *pluginv1.Status) primitives.Result[primitives.Unit] {
	if status != nil && status.Ok {
		return primitives.Success()
	}
	if status == nil || status.Error == nil {
		return primitives.Fail[primitives.Unit](primitives.Unexpected(ErrorCodeUnexpected, MessageUnexpected, nil, map[string]any{"reason": "the status carries no error"}))
	}
	return primitives.Fail[primitives.Unit](FromProtoError(status.Error))
}

// StatusFromError 把回调里的 Go 错误转成失败结果：*primitives.Error 原样，其他为 PLUGINS_UNEXPECTED_001。
func StatusFromError(err error) primitives.Result[primitives.Unit] {
	if err == nil {
		return primitives.Success()
	}
	if typed, ok := err.(*primitives.Error); ok {
		return primitives.Fail[primitives.Unit](typed)
	}
	if framework, ok := err.(*primitives.FrameworkError); ok {
		return primitives.Fail[primitives.Unit](framework.ToError())
	}
	return primitives.Fail[primitives.Unit](primitives.Literal(ErrorCodeUnexpected, err.Error(), nil, nil))
}

// FromMetadata 从协议的上下文元数据构造调用上下文：截止时间取元数据的 RFC 3339 时间，没有时取 gRPC 的截止时间；
// 追踪取 traceparent；Baggage 只收格式合法的键。parent 承载 gRPC 的取消。
func FromMetadata(parent context.Context, metadata *pluginv1.InvocationMetadata) *invocation.Context {
	options := []invocation.Option{}
	if metadata != nil {
		if metadata.TraceParent != "" {
			options = append(options, invocation.WithTraceParent(metadata.TraceParent))
		}
		if metadata.Deadline != "" {
			if deadline, err := time.Parse(time.RFC3339Nano, metadata.Deadline); err == nil {
				options = append(options, invocation.WithDeadline(deadline))
			}
		}
		baggage := map[string]string{}
		for key, value := range metadata.Baggage {
			if primitives.IsBaggageKeyWellFormed(key) {
				baggage[key] = value
			}
		}
		if len(baggage) > 0 {
			options = append(options, invocation.WithBaggage(baggage))
		}
	}
	if metadata == nil || metadata.Deadline == "" {
		if deadline, ok := parent.Deadline(); ok {
			options = append(options, invocation.WithDeadline(deadline))
		}
	}
	return invocation.New(parent, options...)
}

// ToMetadata 把调用上下文编码为协议的上下文元数据：追踪、截止时间与已登记的 Baggage（RFC-0003 规则 3）。
func ToMetadata(ctx *invocation.Context) *pluginv1.InvocationMetadata {
	metadata := &pluginv1.InvocationMetadata{TraceParent: ctx.TraceParent(), Baggage: map[string]string{}}
	if deadline, ok := ctx.Deadline(); ok {
		metadata.Deadline = deadline.UTC().Format(time.RFC3339Nano)
	}
	for key, value := range ctx.WithoutUnregisteredBaggage().Baggage() {
		metadata.Baggage[key] = value
	}
	return metadata
}

// ToProtoEnvelope 把信封编码为协议消息。
func ToProtoEnvelope(envelope Envelope) *pluginv1.Envelope {
	specVersion := envelope.SpecVersion
	if specVersion == "" {
		specVersion = "1.0"
	}
	contentType := envelope.DataContentType
	if contentType == "" {
		contentType = "application/json"
	}
	return &pluginv1.Envelope{
		Topic:           envelope.Topic,
		Id:              envelope.ID,
		Source:          envelope.Source,
		Type:            envelope.Type,
		SpecVersion:     specVersion,
		Subject:         envelope.Subject,
		Time:            envelope.Time,
		DataContentType: contentType,
		Data:            envelope.Data,
		Extensions:      envelope.Extensions,
	}
}

// FromProtoEnvelope 把协议消息还原为信封。
func FromProtoEnvelope(envelope *pluginv1.Envelope) Envelope {
	if envelope == nil {
		return Envelope{}
	}
	return Envelope{
		Topic:           envelope.Topic,
		ID:              envelope.Id,
		Source:          envelope.Source,
		Type:            envelope.Type,
		SpecVersion:     envelope.SpecVersion,
		Subject:         envelope.Subject,
		Time:            envelope.Time,
		DataContentType: envelope.DataContentType,
		Data:            envelope.Data,
		Extensions:      envelope.Extensions,
	}
}

// toProtoHealth 把自报健康编码为协议消息。
func toProtoHealth(health Health) *pluginv1.HealthResponse {
	response := &pluginv1.HealthResponse{State: pluginv1.LifecycleState(health.State), Error: ToProtoError(health.Error)}
	for _, item := range health.Items {
		response.Items = append(response.Items, &pluginv1.HealthItem{Name: item.Name, Status: pluginv1.HealthStatus(item.Status), Description: item.Description})
	}
	return response
}
