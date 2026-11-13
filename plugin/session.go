package plugin

import (
	"context"
	"crypto/subtle"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// sessionGuard 只放行带本次会话令牌的宿主调用（handshake.md §4，ADR-0202）：宿主每次调用 Plugin 服务都在元数据
// xihan-plugin-session 里带 XIHAN_PLUGIN_SESSION，缺失或不匹配以 Unauthenticated 拒绝、不执行；
// 本机别的进程连上插件端点也冒充不了宿主。
type sessionGuard struct {
	token []byte
}

// newSessionGuard 以握手前经环境变量下发的令牌创建守卫。
func newSessionGuard(token string) *sessionGuard {
	return &sessionGuard{token: []byte(token)}
}

// unary 是一元调用的服务端拦截器。
func (g *sessionGuard) unary(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := g.verify(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, request)
}

// stream 是流式调用（WatchHealth、InvokeStream）的服务端拦截器。
func (g *sessionGuard) stream(server any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := g.verify(stream.Context()); err != nil {
		return err
	}
	return handler(server, stream)
}

// verify 核对调用元数据里的会话令牌。
func (g *sessionGuard) verify(ctx context.Context) error {
	incoming, _ := metadata.FromIncomingContext(ctx)
	presented := incoming.Get(SessionMetadataKey)
	// 常数时间比较：逐字节短路的比较会从耗时里泄露令牌前缀；只认恰好一个值，没有令牌的守卫一律拒绝
	if len(g.token) == 0 || len(presented) != 1 || subtle.ConstantTimeCompare([]byte(presented[0]), g.token) != 1 {
		return status.Error(codes.Unauthenticated, "the plugin session token is missing or invalid")
	}
	return nil
}
