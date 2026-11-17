package plugin

import (
	"context"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// Host 是宿主回调服务的客户端（invocation.md §5）：每次调用带会话令牌，宿主以插件身份执行。
// 发出之前在本地按宿主的顺序核对权限与租户，不合即不发请求；没有可用的回调端点（handshake.md §4）时一律以
// PLUGINS_UNAVAILABLE_001 + plugins.host.unavailable 失败。
type Host struct {
	pluginID string
	// declared 是清单的 permissions：回调所需的权限必须在这里（清单是插件权限的唯一事实源，RFC-0017）
	declared []string
	// granted 返回 Configure 下发的 granted_permissions
	granted func() []string
	// inFlight 判断某个租户是不是插件此刻正在处理的调用或投递的租户
	inFlight func(tenant string) bool
	session  string
	endpoint Endpoint
	// conn 与 client 在没有可用的回调端点时为空
	conn   *grpc.ClientConn
	client pluginv1.HostClient
}

// hostKey 是调用上下文对象袋里放 Host 的键。
type hostKey struct{}

// HostOf 从调用上下文取宿主客户端；运行时在每次调用与投递的上下文里放入它（没有可用的回调端点时也放，回调以
// plugins.host.unavailable 失败），不是插件调用的上下文时为假。
func HostOf(ctx *invocation.Context) (*Host, bool) {
	value, ok := ctx.Get(hostKey{})
	if !ok || value == nil {
		return nil, false
	}
	host, ok := value.(*Host)
	return host, ok && host != nil
}

// Endpoint 返回选中的宿主回调端点；没有可用的回调端点时为零值。
func (h *Host) Endpoint() Endpoint { return h.endpoint }

// Invoke 以插件身份经宿主完整管道调用操作 id（需要权限 capability:kernel.pipeline）。失败按 invocation.md §5：
// 本地核对不合（plugins.callback.denied、plugins.callback.tenant_denied）或没有回调端点时不发请求；gRPC 失败按映射表
// （rules.MapCallbackStatus，向量 callback-status.json）；宿主写来的 Status 按 FromStatus 读。
func (h *Host) Invoke(ctx *invocation.Context, id string, payload []byte) operation.Response {
	if failure := h.check(ctx, rules.CallbackInvoke); failure != nil {
		return operation.Response{Outcome: primitives.Fail[primitives.Unit](failure)}
	}
	callCtx, cancel := h.outgoing(ctx)
	defer cancel()
	response, err := h.client.Invoke(callCtx, &pluginv1.InvokeRequest{Operation: id, Payload: payload, Metadata: ToMetadata(ctx)})
	if err != nil {
		return operation.Response{Outcome: primitives.Fail[primitives.Unit](h.failure(ctx, rules.CallbackInvoke, err, id))}
	}
	outcome := FromStatus(response.Status, h.pluginID, id)
	if !outcome.OK() {
		return operation.Response{Outcome: outcome}
	}
	return operation.Response{Outcome: outcome, Payload: response.Payload}
}

// Publish 经宿主消息代理发布消息（需要权限 capability:messaging.queue）；结果与信封一一对应，空的信封列表不发请求、返回空切片。
// 本地核对、没有回调端点与 RPC 失败都是整批的结果，每一项都是同一个错误；宿主返回的结果数与信封数不同时每一项都是
// plugins.process.failed（phase 为 publish）。信封的 xihantenant 不在本地核对，由宿主逐项判定。
func (h *Host) Publish(ctx *invocation.Context, envelopes ...Envelope) []primitives.Result[primitives.Unit] {
	results := make([]primitives.Result[primitives.Unit], len(envelopes))
	every := func(err *primitives.Error) []primitives.Result[primitives.Unit] {
		for index := range results {
			results[index] = primitives.Fail[primitives.Unit](err)
		}
		return results
	}
	if len(envelopes) == 0 {
		return results
	}
	if failure := h.check(ctx, rules.CallbackPublish); failure != nil {
		return every(failure)
	}
	request := &pluginv1.PublishRequest{Metadata: ToMetadata(ctx)}
	for _, envelope := range envelopes {
		request.Envelopes = append(request.Envelopes, ToProtoEnvelope(envelope))
	}
	callCtx, cancel := h.outgoing(ctx)
	defer cancel()
	response, err := h.client.Publish(callCtx, request)
	if err != nil {
		return every(h.failure(ctx, rules.CallbackPublish, err, ""))
	}
	if len(response.Results) != len(envelopes) {
		return every(processFailed(h.pluginID, phasePublish, reasonResultCount(len(response.Results), len(envelopes))))
	}
	for index, result := range response.Results {
		results[index] = FromStatus(result, h.pluginID, phasePublish)
	}
	return results
}

// Close 断开与宿主的连接。
func (h *Host) Close() error {
	if h.conn == nil {
		return nil
	}
	return h.conn.Close()
}

// check 在发出之前核对（invocation.md §5）。没有可用的回调端点时每次回调都是 plugins.host.unavailable（handshake.md §4 的
// 「每次回调」），先于其他判定；然后按宿主的顺序：权限（既在清单 permissions 里、也在 granted_permissions 里），
// 租户（回调上下文的 xihan.tenant 必须是某个在途调用或投递的租户），与宿主给出同样的错误。
func (h *Host) check(ctx *invocation.Context, rpc string) *primitives.Error {
	if h.client == nil {
		return hostUnavailable(h.pluginID, reasonNoCallbackEndpoint)
	}
	permission := rules.CallbackPermission(rpc)
	if !slices.Contains(h.declared, permission) || !slices.Contains(h.granted(), permission) {
		return callbackDenied(h.pluginID, permission)
	}
	if tenant := ctx.Tenant(); tenant != "" && !h.inFlight(tenant) {
		return tenantDenied(h.pluginID, tenant)
	}
	return nil
}

// outgoing 组装回调的 gRPC 上下文：带会话令牌；gRPC 的截止时间与写进元数据的 deadline 相同（invocation.md §5），
// 写出的 deadline 截到 100 纳秒，所以按写出再读回的时刻设置，而不是直接用上下文里更精细的时刻。
func (h *Host) outgoing(ctx *invocation.Context) (context.Context, context.CancelFunc) {
	var base context.Context = ctx
	cancel := context.CancelFunc(func() {})
	if deadline, ok := ctx.Deadline(); ok {
		if written, ok := rules.ParseDeadline(rules.FormatDeadline(deadline)); ok {
			base, cancel = context.WithDeadline(base, written)
		}
	}
	return metadata.AppendToOutgoingContext(base, SessionMetadataKey, h.session), cancel
}

// failure 按 invocation.md §5 的表映射回调的 gRPC 失败；不是 gRPC 状态的错误按连接失败（Unavailable）处理。
// Cancelled 看回调所在的调用上下文：取消信号已触发是取消，没有触发是连接被宿主或网络切断
func (h *Host) failure(ctx *invocation.Context, rpc string, err error, operationID string) *primitives.Error {
	code, message := codes.Unavailable, err.Error()
	if grpcStatus, ok := status.FromError(err); ok {
		code, message = grpcStatus.Code(), grpcStatus.Message()
	}
	mapped := rules.MapCallbackStatus(rpc, uint32(code), message, ctx.Err() != nil, h.pluginID, operationID)
	var details map[string]any
	if mapped.Details != nil {
		details = make(map[string]any, len(mapped.Details))
		for name, value := range mapped.Details {
			details[name] = value
		}
	}
	return primitives.NewError(mapped.Code, mapped.Key, details, nil)
}
