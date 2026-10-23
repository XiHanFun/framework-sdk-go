package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/manifest"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// selfReportedStates 是插件可以自报的状态（lifecycle.md §1）。
var selfReportedStates = []primitives.LifecycleState{primitives.Started, primitives.Ready, primitives.Degraded, primitives.Draining, primitives.Failed}

// runtime 是一次 Serve 的状态。
type runtime struct {
	opts          Options
	manifest      *manifest.Manifest
	invoker       *operation.Invoker
	subscriptions []SubscriptionHandler
	lifecycle     *lifecycle
	logger        *slog.Logger
	server        *grpc.Server
	host          *Host
	session       string
	instance      string
	locale        string
	hostVersions  []uint32
	stopOnce      sync.Once
	stopped       chan struct{}
}

// service 实现 xihan.plugin.v1.Plugin。
type service struct {
	pluginv1.UnimplementedPluginServer
	r *runtime
}

// negotiate 选宿主列表里最高的本 SDK 也支持的协议版本。
func negotiate(host []uint32) (uint32, bool) {
	var selected uint32
	found := false
	for _, candidate := range host {
		if candidate == ProtocolVersion && (!found || candidate > selected) {
			selected = candidate
			found = true
		}
	}
	return selected, found
}

// Handshake 协商版本、报告原语层版本并交换清单、操作、订阅与目录（handshake.md §3）。
func (s *service) Handshake(_ context.Context, request *pluginv1.HandshakeRequest) (*pluginv1.HandshakeResponse, error) {
	if !s.r.lifecycle.is(primitives.Declared, primitives.Failed) {
		return nil, status.Error(codes.FailedPrecondition, "the plugin has already completed its handshake")
	}
	selected, ok := negotiate(request.ProtocolVersions)
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "no common protocol version: the host supports %v, the plugin supports %d", request.ProtocolVersions, ProtocolVersion)
	}
	response := &pluginv1.HandshakeResponse{
		ProtocolVersion: selected,
		Primitives:      uint32(s.r.manifest.Primitives),
		Manifest:        string(s.r.opts.Manifest),
		Sdk:             &pluginv1.SdkInfo{Name: primitives.SDKName, Version: primitives.SDKVersion, Language: "go"},
	}
	for _, descriptor := range s.r.invoker.Registry().List() {
		declaration := &pluginv1.OperationDeclaration{
			Id:         descriptor.Contract.ID,
			Stability:  descriptor.Contract.Stability.String(),
			Expose:     descriptor.Contract.Expose,
			Permission: descriptor.Contract.Permission,
			Policy:     descriptor.Contract.Policy,
		}
		if descriptor.Schema != nil {
			declaration.RequestSchema = descriptor.Schema.Request
			declaration.ResponseSchema = descriptor.Schema.Response
		}
		response.Operations = append(response.Operations, declaration)
	}
	for _, subscription := range s.r.subscriptions {
		response.Subscriptions = append(response.Subscriptions, &pluginv1.Subscription{Topic: subscription.Subscription.Topic, MessageType: subscription.Subscription.MessageType})
	}
	for _, catalog := range s.r.opts.Catalogs {
		encoded := &pluginv1.MessageCatalog{Name: catalog.Name}
		for _, locale := range sortedKeys(catalog.Locales) {
			encoded.Locales = append(encoded.Locales, &pluginv1.LocaleTemplates{Locale: locale, Templates: catalog.Locales[locale]})
		}
		response.Catalogs = append(response.Catalogs, encoded)
	}
	s.r.lifecycle.transition(primitives.Resolved, nil)
	s.r.logger.Info("handshake completed", "protocolVersion", selected, "host", request.GetHost().GetSdk(), "instance", request.InstanceId)
	return response, nil
}

// Configure 下发配置、权限、绑定与工作目录（lifecycle.md §2）。
func (s *service) Configure(ctx context.Context, request *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	if !s.r.lifecycle.is(primitives.Resolved) {
		return nil, status.Error(codes.FailedPrecondition, "Configure requires a completed handshake")
	}
	configuration := Configuration{
		Section:            json.RawMessage(request.Configuration),
		GrantedPermissions: request.GrantedPermissions,
		WorkDirectory:      request.WorkDirectory,
	}
	if len(configuration.Section) == 0 {
		configuration.Section = json.RawMessage("{}")
	}
	for _, binding := range request.Bindings {
		configuration.Bindings = append(configuration.Bindings, Binding{Name: binding.Name, Type: binding.Type, Value: binding.Value})
	}
	outcome := primitives.Success()
	if s.r.opts.Hooks.Configure != nil {
		outcome = StatusFromError(s.r.opts.Hooks.Configure(ctx, configuration))
	}
	if outcome.OK() {
		s.r.lifecycle.transition(primitives.Configured, nil)
	} else {
		s.r.lifecycle.transition(primitives.Failed, outcome.Err())
		s.r.logger.Error("configure failed", "code", outcome.Err().Code, "message", outcome.Err().Message())
	}
	return &pluginv1.ConfigureResponse{Status: ToStatus(outcome)}, nil
}

// Start 启动；返回 ok 后接受调用。
func (s *service) Start(ctx context.Context, _ *pluginv1.StartRequest) (*pluginv1.StartResponse, error) {
	if !s.r.lifecycle.is(primitives.Configured) {
		return nil, status.Error(codes.FailedPrecondition, "Start requires a successful Configure")
	}
	outcome := primitives.Success()
	if s.r.opts.Hooks.Start != nil {
		outcome = StatusFromError(s.r.opts.Hooks.Start(ctx))
	}
	if outcome.OK() {
		// 没有自报健康的插件直接在 Start 后报 Ready（lifecycle.md §1）
		s.r.lifecycle.transition(primitives.Started, nil)
		s.r.lifecycle.transition(primitives.Ready, nil)
		s.r.logger.Info("started", "operations", len(s.r.invoker.Registry().List()), "subscriptions", len(s.r.subscriptions))
	} else {
		s.r.lifecycle.transition(primitives.Failed, outcome.Err())
		s.r.logger.Error("start failed", "code", outcome.Err().Code, "message", outcome.Err().Message())
	}
	return &pluginv1.StartResponse{Status: ToStatus(outcome)}, nil
}

// Stop 进入 Draining：完成在途调用、释放资源、返回，然后退出进程（lifecycle.md §5）。
func (s *service) Stop(ctx context.Context, request *pluginv1.StopRequest) (*pluginv1.StopResponse, error) {
	grace := time.Duration(request.GraceSeconds) * time.Second
	if grace <= 0 {
		grace = s.r.opts.StopGrace
	}
	s.r.lifecycle.transition(primitives.Draining, nil)
	outcome := primitives.Success()
	if s.r.opts.Hooks.Stop != nil {
		outcome = StatusFromError(s.r.opts.Hooks.Stop(ctx, request.Reason, grace))
	}
	s.r.logger.Info("stopping", "reason", request.Reason, "grace", grace.String())
	// 应答送出后再优雅停止：GracefulStop 会等本次 RPC 结束
	go s.r.shutdown(grace)
	return &pluginv1.StopResponse{Status: ToStatus(outcome)}, nil
}

// Health 返回当前健康。
func (s *service) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return toProtoHealth(s.r.health()), nil
}

// WatchHealth 先推当前状态，之后状态或健康项变化时各推一条。
func (s *service) WatchHealth(_ *pluginv1.HealthRequest, stream grpc.ServerStreamingServer[pluginv1.HealthResponse]) error {
	changes, unwatch := s.r.lifecycle.watch()
	defer unwatch()
	last := s.r.health()
	if err := stream.Send(toProtoHealth(last)); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-s.r.stopped:
			return nil
		case <-changes:
		case <-ticker.C:
		}
		current := s.r.health()
		if reflect.DeepEqual(current, last) {
			continue
		}
		last = current
		if err := stream.Send(toProtoHealth(current)); err != nil {
			return err
		}
	}
}

// Invoke 调用一个一元操作：未就绪返回 PLUGINS_UNAVAILABLE_001，未声明的操作以 Unimplemented 结束。
func (s *service) Invoke(ctx context.Context, request *pluginv1.InvokeRequest) (*pluginv1.InvokeResponse, error) {
	if !s.r.serving() {
		return &pluginv1.InvokeResponse{Status: ToStatus(s.r.unavailable())}, nil
	}
	if _, ok := s.r.invoker.Registry().Get(request.Operation); !ok {
		return nil, status.Errorf(codes.Unimplemented, "the operation '%s' is not provided by this plugin", request.Operation)
	}
	invocationCtx := s.r.newContext(ctx, request.Metadata)
	response := s.r.invoker.Invoke(invocationCtx, operation.Request{ID: request.Operation, Payload: request.Payload})
	return &pluginv1.InvokeResponse{Status: ToStatus(response.Outcome), Payload: response.Payload}, nil
}

// InvokeStream 在本版本不提供流式操作。
func (s *service) InvokeStream(grpc.BidiStreamingServer[pluginv1.StreamFrame, pluginv1.StreamFrame]) error {
	return status.Error(codes.Unimplemented, "streaming operations are not supported by this SDK version")
}

// HandleEvent 把消息投递给匹配的订阅处理器。
func (s *service) HandleEvent(ctx context.Context, request *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error) {
	if !s.r.serving() {
		return &pluginv1.HandleEventResponse{Status: ToStatus(s.r.unavailable())}, nil
	}
	envelope := FromProtoEnvelope(request.Envelope)
	handler, ok := s.r.handlerFor(envelope)
	if !ok {
		return &pluginv1.HandleEventResponse{Status: ToStatus(primitives.Fail[primitives.Unit](primitives.NotFound(operation.ErrorCodeNotFound, operation.MessageNotFound, map[string]any{"topic": envelope.Topic, "type": envelope.Type})))}, nil
	}
	invocationCtx := s.r.newContext(ctx, request.Metadata)
	return &pluginv1.HandleEventResponse{Status: ToStatus(handler(invocationCtx, envelope, request.Attempt))}, nil
}

// serving 判断是否接受调用。
func (r *runtime) serving() bool {
	return r.lifecycle.is(primitives.Started, primitives.Ready, primitives.Degraded)
}

// unavailable 创建未就绪的失败。
func (r *runtime) unavailable() primitives.Result[primitives.Unit] {
	state, _ := r.lifecycle.current()
	return primitives.Fail[primitives.Unit](primitives.Unavailable(ErrorCodeUnavailable, MessageUnavailable, map[string]any{"id": r.manifest.ID, "state": state.String()}))
}

// newContext 从 gRPC 上下文与协议元数据构造调用上下文，并放入宿主客户端。
func (r *runtime) newContext(parent context.Context, metadata *pluginv1.InvocationMetadata) *invocation.Context {
	ctx := FromMetadata(parent, metadata)
	if r.host != nil {
		ctx.Set(hostKey{}, r.host)
	}
	return ctx
}

// handlerFor 按主题与可选的消息类型找订阅处理器。
func (r *runtime) handlerFor(envelope Envelope) (EventHandler, bool) {
	for _, subscription := range r.subscriptions {
		if subscription.Subscription.Topic == envelope.Topic && (subscription.Subscription.MessageType == "" || subscription.Subscription.MessageType == envelope.Type) {
			return subscription.Handler, true
		}
	}
	return nil, false
}

// health 计算当前健康：没有回调时按运行时状态；回调报的状态只能是可自报的状态，健康项只保留清单声明的名字；
// 回调报 Failed / Degraded / Ready 时同步运行时状态。
func (r *runtime) health() Health {
	state, err := r.lifecycle.current()
	if r.opts.Hooks.Health == nil {
		return Health{State: state, Error: err}
	}
	reported := r.opts.Hooks.Health()
	if !slices.Contains(selfReportedStates, reported.State) {
		reported.State = state
	}
	declared := r.manifest.Health()
	kept := reported.Items[:0:0]
	for _, item := range reported.Items {
		if slices.Contains(declared, item.Name) {
			kept = append(kept, item)
		} else {
			r.logger.Warn("health item is not declared in the manifest", "item", item.Name)
		}
	}
	reported.Items = kept
	switch reported.State {
	case primitives.Failed:
		r.lifecycle.transition(primitives.Failed, reported.Error)
	case primitives.Degraded, primitives.Ready:
		if state == primitives.Ready || state == primitives.Degraded {
			r.lifecycle.transition(reported.State, reported.Error)
		}
	}
	if current, _ := r.lifecycle.current(); current != reported.State && !slices.Contains(selfReportedStates, current) {
		reported.State = current
	}
	return reported
}

// shutdown 优雅停止 gRPC 服务端（宽限内等在途调用），然后标记 Stopped 并让 Serve 返回；只执行一次。
func (r *runtime) shutdown(grace time.Duration) {
	r.stopOnce.Do(func() {
		done := make(chan struct{})
		go func() {
			r.server.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(grace):
			r.logger.Warn("in-flight calls did not finish within the grace period; stopping")
			r.server.Stop()
		}
		r.lifecycle.transition(primitives.Stopped, nil)
		close(r.stopped)
	})
}

// sortedKeys 返回按升序排列的键。
func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// describeState 便于日志。
func describeState(state primitives.LifecycleState) string { return fmt.Sprint(state) }
