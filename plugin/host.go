package plugin

import (
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// Host 是宿主回调服务的客户端（invocation.md §5）：每次调用带会话令牌，宿主以插件身份执行。
type Host struct {
	endpoint Endpoint
	session  string
	conn     *grpc.ClientConn
	client   pluginv1.HostClient
}

// hostKey 是调用上下文对象袋里放 Host 的键。
type hostKey struct{}

// newHost 连接宿主回调端点；连接是惰性的，首次调用时才真正建立。
func newHost(endpoint Endpoint, session string) (*Host, error) {
	conn, err := dial(endpoint)
	if err != nil {
		return nil, err
	}
	return &Host{endpoint: endpoint, session: session, conn: conn, client: pluginv1.NewHostClient(conn)}, nil
}

// HostOf 从调用上下文取宿主客户端；运行时在每次调用与投递的上下文里放入它，没有回调端点时为假。
func HostOf(ctx *invocation.Context) (*Host, bool) {
	value, ok := ctx.Get(hostKey{})
	if !ok || value == nil {
		return nil, false
	}
	host, ok := value.(*Host)
	return host, ok && host != nil
}

// Endpoint 返回宿主回调端点。
func (h *Host) Endpoint() Endpoint { return h.endpoint }

// Invoke 以插件身份经宿主完整管道调用操作（需要权限 capability:kernel.pipeline）；传输层失败映射为原语层错误。
func (h *Host) Invoke(ctx *invocation.Context, id string, payload []byte) operation.Response {
	response, err := h.client.Invoke(h.outgoing(ctx), &pluginv1.InvokeRequest{Operation: id, Payload: payload, Metadata: ToMetadata(ctx)})
	if err != nil {
		return operation.Response{Outcome: primitives.Fail[primitives.Unit](FromGrpcError(err))}
	}
	outcome := FromStatus(response.Status)
	if !outcome.OK() {
		return operation.Response{Outcome: outcome}
	}
	return operation.Response{Outcome: outcome, Payload: response.Payload}
}

// Publish 经宿主消息代理发布消息（需要权限 capability:messaging.queue）；结果与信封一一对应。
func (h *Host) Publish(ctx *invocation.Context, envelopes ...Envelope) []primitives.Result[primitives.Unit] {
	request := &pluginv1.PublishRequest{Metadata: ToMetadata(ctx)}
	for _, envelope := range envelopes {
		request.Envelopes = append(request.Envelopes, ToProtoEnvelope(envelope))
	}
	results := make([]primitives.Result[primitives.Unit], len(envelopes))
	response, err := h.client.Publish(h.outgoing(ctx), request)
	if err != nil {
		failure := primitives.Fail[primitives.Unit](FromGrpcError(err))
		for index := range results {
			results[index] = failure
		}
		return results
	}
	for index := range results {
		if index < len(response.Results) {
			results[index] = FromStatus(response.Results[index])
		} else {
			results[index] = primitives.Fail[primitives.Unit](primitives.Unexpected(ErrorCodeUnexpected, MessageUnexpected, nil, map[string]any{"reason": "the host returned fewer results than envelopes"}))
		}
	}
	return results
}

// Close 断开与宿主的连接。
func (h *Host) Close() error { return h.conn.Close() }

// outgoing 给回调加会话令牌元数据。
func (h *Host) outgoing(ctx *invocation.Context) *invocation.Context {
	derived := *ctx
	derived.Context = metadata.AppendToOutgoingContext(ctx.Context, SessionMetadataKey, h.session)
	return &derived
}

// FromGrpcError 把 gRPC 状态映射为原语层错误（invocation.md §3 的映射表，从插件视角看宿主）。
func FromGrpcError(err error) *primitives.Error {
	if err == nil {
		return nil
	}
	if translated := invocation.FromContextError(err); translated != nil {
		return translated
	}
	code := codes.Unknown
	message := err.Error()
	if grpcStatus, ok := status.FromError(err); ok {
		code = grpcStatus.Code()
		message = grpcStatus.Message()
	}
	switch code {
	case codes.Unavailable:
		return primitives.Unavailable(ErrorCodeUnavailable, MessageUnavailable, map[string]any{"reason": message})
	case codes.DeadlineExceeded:
		return primitives.Timeout(invocation.ErrorCodeDeadlineExceeded, invocation.MessageDeadlineExceeded, nil)
	case codes.Canceled:
		return primitives.Cancelled(invocation.ErrorCodeCancelled, invocation.MessageCancelled, nil)
	case codes.PermissionDenied, codes.Unauthenticated:
		return primitives.Denied(ErrorCodeCallbackDenied, MessageCallbackDenied, map[string]any{"reason": message})
	case codes.Unimplemented:
		return primitives.NotFound(operation.ErrorCodeNotFound, operation.MessageNotFound, map[string]any{"reason": message})
	case codes.InvalidArgument:
		return primitives.Validation(operation.ErrorCodeInvalidPayload, operation.MessageInvalidPayload, map[string]any{"reason": message})
	default:
		var connection *primitives.Error
		if errors.As(err, &connection) {
			return connection
		}
		return primitives.Unexpected(ErrorCodeUnexpected, MessageUnexpected, nil, map[string]any{"status": code.String(), "reason": message})
	}
}
