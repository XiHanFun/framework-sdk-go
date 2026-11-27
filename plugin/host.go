package plugin

import (
	"context"
	"maps"
	"slices"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
// PLUGINS_UNAVAILABLE_001 + plugins.host.unavailable 失败；Close 之后（Serve 返回时运行时关闭它）的回调也不发请求，
// 同一个错误，reason 为 Unavailable: the host client is closed（ADR-0349）。
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
	// closed 在 Close 之后为真（invocation.md §5「回调客户端已关闭」，ADR-0349）：之后的回调不经 grpc-go，
	// 被关闭切断的在途回调也按它报，不出现 grpc-go 自己的文字
	closed atomic.Bool
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
// 本地核对不合（plugins.callback.denied、plugins.callback.tenant_denied、元数据写不出的 plugins.process.failed）、没有回调端点
// 或客户端已关闭时不发请求，先后见 check；gRPC 失败按映射表（rules.MapCallbackStatus，向量 callback-status.json）；
// 宿主写来的 Status 按 FromStatus 读。
func (h *Host) Invoke(ctx *invocation.Context, id string, payload []byte) operation.Response {
	if failure := h.check(ctx, rules.CallbackInvoke, id); failure != nil {
		return operation.Response{Outcome: primitives.Fail[primitives.Unit](failure)}
	}
	callCtx, cancel := h.outgoing(ctx)
	defer cancel()
	response, err := h.client.Invoke(callCtx, &pluginv1.InvokeRequest{Operation: id, Payload: payload, Metadata: ToMetadata(ctx)})
	if err != nil {
		return operation.Response{Outcome: primitives.Fail[primitives.Unit](h.failure(ctx, rules.CallbackInvoke, err, id))}
	}
	outcome := FromStatus(response.Status, h.pluginID, rules.CallbackPhase(rules.CallbackInvoke, id))
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
	if failure := h.check(ctx, rules.CallbackPublish, ""); failure != nil {
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
	phase := rules.CallbackPhase(rules.CallbackPublish, "")
	if len(response.Results) != len(envelopes) {
		return every(processFailed(h.pluginID, phase, reasonResultCount(len(response.Results), len(envelopes))))
	}
	for index, result := range response.Results {
		results[index] = FromStatus(result, h.pluginID, phase)
	}
	return results
}

// Close 断开与宿主的连接。之后的回调不发请求、不重新连接，以 PLUGINS_UNAVAILABLE_001 + plugins.host.unavailable 失败
// （reason 为 Unavailable: the host client is closed，invocation.md §5，ADR-0349），被它切断的在途回调也是这个结果
// （截止时间已到或调用方已取消的照映射表相应的行）；再次调用什么也不做、返回 nil。
func (h *Host) Close() error {
	// 先记下已关闭再关连接：被关闭切断的回调拿到 grpc-go 的错误时，一定也看得到已关闭
	if h.closed.Swap(true) || h.conn == nil {
		return nil
	}
	return h.conn.Close()
}

// check 在发出之前依次核对，不合即不发请求（invocation.md §5）：① 有没有可用的回调端点（handshake.md §4）；② 权限（既在清单
// permissions 里、也在 granted_permissions 里）；③ 租户（回调上下文的 xihan.tenant 必须是某个在途调用或投递的租户），②③ 与宿主给出
// 同样的错误；④ 回调上下文的截止时间已过或已被取消，结果与映射表相应的一行相同；⑤ 写进 metadata.baggage 的值都是合法的 UTF-8
// （ADR-0347）；⑥ 客户端没有关闭（ADR-0349）。Go 的载荷与信封 data 是字节，原样发出，没有编码这一步。operationID 是 Host.Invoke 的操作 ID。
func (h *Host) check(ctx *invocation.Context, rpc, operationID string) *primitives.Error {
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
	if ended := h.ended(ctx, rpc, operationID); ended != nil {
		return ended
	}
	// 元数据排在截止时间之后：权限、租户与截止时间是宿主会给出的错误，写不出请求是本地才有的。protobuf-go 拒绝编码不是合法
	// UTF-8 的 string 字段，不先核对时回调以 grpc-go 的 Internal 失败，reason 是它的文字
	if key, ok := unwritableBaggage(ctx); ok {
		return processFailed(h.pluginID, rules.CallbackPhase(rpc, operationID), reasonLoneSurrogateBaggage(key))
	}
	// 已关闭不是发出之前的核对而是发出这一步本身，排在其余各项之后（ADR-0328）
	if h.closed.Load() {
		return h.closedFailure(rpc, operationID)
	}
	return nil
}

// ended 是 check 的第 ④ 步：回调上下文的截止时间已到（截止令牌已触发或按时钟已过）按 DeadlineExceeded，否则已被取消按
// 「Cancelled，取消信号已触发」，与映射表相应的一行相同（invocation.md §5，ADR-0246）；都没有时为 nil。
func (h *Host) ended(ctx *invocation.Context, rpc, operationID string) *primitives.Error {
	// 截止时间先于取消判定：截止时间到了时上下文也随之结束，按截止令牌归因（RFC-0004），与宿主一侧的 DeadlineMiddleware 一致
	if deadline, ok := ctx.Deadline(); ctx.DeadlineExceeded() || (ok && !time.Now().Before(deadline)) {
		return h.mapStatus(rpc, codes.DeadlineExceeded, "the deadline of the callback context has passed", true, operationID)
	}
	if ctx.Err() != nil {
		return h.mapStatus(rpc, codes.Canceled, "the callback context is cancelled", true, operationID)
	}
	return nil
}

// unwritableBaggage 返回写进回调 metadata.baggage（上下文里已登记的键，与 ToMetadata 同一个来源）的值里第一个不是合法 UTF-8 的键
// （invocation.md §5，ADR-0347）。Go 的字符串以字节存放，编成 UTF-8 的孤立代理项也是不合法的 UTF-8；上下文里只有幂等键可能带着这样的值。
// 几个键都是这样时点名哪一个规范不作规定，这里按键名排序取第一个，只为结果确定；只看是不是合法的 UTF-8，不按 Baggage 值的完整约束判定
// （幂等键不受长度与控制字符的约束）。
func unwritableBaggage(ctx *invocation.Context) (string, bool) {
	baggage := ctx.WithoutUnregisteredBaggage().Baggage()
	for _, key := range slices.Sorted(maps.Keys(baggage)) {
		if !utf8.ValidString(baggage[key]) {
			return key, true
		}
	}
	return "", false
}

// closedFailure 是回调客户端已关闭的结果（invocation.md §5「回调客户端已关闭」，ADR-0328、ADR-0349）：与连接失败同一行，
// PLUGINS_UNAVAILABLE_001 + plugins.host.unavailable，reason 写成 Unavailable: the host client is closed，状态名与说明与
// TypeScript、Python、PHP、Java 相同。
func (h *Host) closedFailure(rpc, operationID string) *primitives.Error {
	return h.mapStatus(rpc, codes.Unavailable, closedClientMessage, false, operationID)
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

// failure 按 invocation.md §5 的表映射回调的 gRPC 失败；不是 gRPC 状态的错误按连接失败（Unavailable）处理。grpc-go 在本端
// 按截止时间中止调用时报的就是 DeadlineExceeded，不用再归一；只按表映射，不另按时钟或令牌改写（超时的归因交给插件自己的管道，RFC-0003 规则 1）。
// Cancelled 看回调所在的调用上下文：取消信号已触发是取消，没有触发是连接被宿主或网络切断
func (h *Host) failure(ctx *invocation.Context, rpc string, err error, operationID string) *primitives.Error {
	code, message := codes.Unavailable, err.Error()
	if grpcStatus, ok := status.FromError(err); ok {
		code, message = grpcStatus.Code(), grpcStatus.Message()
	}
	// Close 切断了在途的回调，或在 check 之后、发出之前关闭时，grpc-go 以 Cancelled 报它自己的文字（grpc: the client connection is closing）：
	// 与已关闭同一行、同一句 reason，截止时间已到或调用方已取消的照映射表相应的行（invocation.md §5，ADR-0349）。只换这两个状态：
	// 别的状态是宿主给出的应答，照常映射
	if h.closed.Load() && (code == codes.Canceled || code == codes.Unavailable) {
		if ended := h.ended(ctx, rpc, operationID); ended != nil {
			return ended
		}
		return h.closedFailure(rpc, operationID)
	}
	return h.mapStatus(rpc, code, message, ctx.Err() != nil, operationID)
}

// mapStatus 按映射表（rules.MapCallbackStatus，向量 callback-status.json）把一个 gRPC 状态换成错误。
func (h *Host) mapStatus(rpc string, code codes.Code, message string, contextCancelled bool, operationID string) *primitives.Error {
	mapped := rules.MapCallbackStatus(rpc, uint32(code), message, contextCancelled, h.pluginID, operationID)
	var details map[string]any
	if mapped.Details != nil {
		details = make(map[string]any, len(mapped.Details))
		for name, value := range mapped.Details {
			details[name] = value
		}
	}
	return primitives.NewError(mapped.Code, mapped.Key, details, nil)
}
