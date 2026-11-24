package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/manifest"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// 一次性 RPC 的名字（lifecycle.md §7.1）：每个在进程里只接受一次。
const (
	stepHandshake = "handshake"
	stepConfigure = "configure"
	stepStart     = "start"
	stepStop      = "stop"
)

// 收尾的时限（lifecycle.md §7.5、§7.7）。
const (
	// defaultStopGrace 是 grace_seconds 为 0 时等在途调用的宽限。
	defaultStopGrace = 5 * time.Second
	// shutdownLimit 是收到 stdin 关闭或 SIGTERM 之后到退出的上限，短于宿主发出终止信号后再等的 5 秒；
	// Stop 应答之后关闭服务端也以它为限。
	shutdownLimit = 3 * time.Second
	// maxWarnedHealthItems 是记过告警的未声明健康项名的上限。
	maxWarnedHealthItems = 256
)

// healthInterval 是可服务期间 WatchHealth 重新求健康回调的间隔（lifecycle.md §7.4，宿主 HealthInterval 的缺省值）；
// Serve 开始时读一次，测试在启动之前改短它。
var healthInterval = 10 * time.Second

// stoppableStates 是接受 Stop 的状态：Declared、Draining、Stopped 之外的全部（lifecycle.md §7.1）。
var stoppableStates = []primitives.LifecycleState{primitives.Resolved, primitives.Configured, primitives.Started, primitives.Ready, primitives.Degraded, primitives.Failed}

// runtime 是一次 Serve 的状态。
type runtime struct {
	opts           Options
	manifest       *manifest.Manifest
	manifestText   string
	invoker        *operation.Invoker
	subscriptions  []SubscriptionHandler
	lifecycle      *lifecycle
	calls          *calls
	logger         *slog.Logger
	server         *grpc.Server
	host           *Host
	session        string
	instance       string
	locale         string
	hostVersions   []uint32
	healthInterval time.Duration

	grantedMu sync.RWMutex
	granted   []string

	// healthMu 让健康回调一次只执行一个：并发的 Health 与 WatchHealth 各自求回调时，旧的结果不会盖过新的
	healthMu sync.Mutex
	warnedMu sync.Mutex
	warned   map[string]bool

	// started 记下启动回调成功过：只有这样才调停止回调（lifecycle.md §7.5）
	started      atomic.Bool
	stopOnce     sync.Once
	stopOutcome  primitives.Result[primitives.Unit]
	shutdownOnce sync.Once
	finishOnce   sync.Once
	// ended 在应答 Stop 或收尾开始关闭服务端时关闭，WatchHealth 流随之结束
	ended chan struct{}
	// stopped 在服务端关闭之后关闭，Serve 随之返回
	stopped chan struct{}
}

// service 实现 xihan.plugin.v1.Plugin。
type service struct {
	pluginv1.UnimplementedPluginServer
	r *runtime
}

// negotiate 选宿主列表里最高的本 SDK 也支持的协议版本（handshake.md §3）：规则是一致性模块的 rules.NegotiateVersion，
// 本 SDK 支持的只有 ProtocolVersion。
func negotiate(host []uint32) (uint32, bool) {
	return rules.NegotiateVersion(host, []uint32{ProtocolVersion})
}

// Handshake 协商版本、报告原语层版本并交换清单、操作、订阅与目录（handshake.md §3），只在 Declared 接受一次。
func (s *service) Handshake(_ context.Context, request *pluginv1.HandshakeRequest) (*pluginv1.HandshakeResponse, error) {
	r := s.r
	selected, ok := negotiate(request.ProtocolVersions)
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "no common protocol version: the host supports %v, the plugin supports %d", request.ProtocolVersions, ProtocolVersion)
	}
	if !r.lifecycle.claim(stepHandshake, primitives.Declared) {
		return nil, status.Error(codes.FailedPrecondition, "Handshake is accepted once, before anything else")
	}
	response := &pluginv1.HandshakeResponse{
		ProtocolVersion: selected,
		// 清单的 primitives，Serve 已核对它等于本 SDK 支持的原语层大版本（handshake.md §1、§3 第 2 条）
		Primitives: uint32(r.manifest.Primitives),
		Manifest:   r.manifestText,
		Sdk:        &pluginv1.SdkInfo{Name: primitives.SDKName, Version: primitives.SDKVersion, Language: "go"},
	}
	// 只有一元操作：流式操作等流式契约的 RFC（handshake.md §3 第 4 条），声明里的 streaming 总是假
	for _, descriptor := range r.invoker.Registry().List() {
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
	// 按声明的顺序写订阅，方式总是写出 competing 或 broadcast（第 5 条）
	for _, subscription := range r.subscriptions {
		declared := subscription.Subscription
		response.Subscriptions = append(response.Subscriptions, &pluginv1.Subscription{Topic: declared.Topic, MessageType: declared.MessageType, Mode: string(declared.Mode.orCompeting())})
	}
	// 只发作者的目录：SDK 运行时的键由宿主提供（第 6 条）
	for _, catalog := range r.opts.Catalogs {
		encoded := &pluginv1.MessageCatalog{Name: catalog.Name}
		for _, locale := range sortedKeys(catalog.Locales) {
			encoded.Locales = append(encoded.Locales, &pluginv1.LocaleTemplates{Locale: locale, Templates: catalog.Locales[locale]})
		}
		response.Catalogs = append(response.Catalogs, encoded)
	}
	r.lifecycle.transition(primitives.Resolved, nil)
	r.logger.Info("handshake completed", "protocolVersion", selected, "host", request.GetHost().GetSdk(), "instance", request.InstanceId)
	return response, nil
}

// Configure 核对配置节并交给作者（lifecycle.md §7.2），只在 Resolved 接受一次：配置节为空等同 {}；不是 JSON 对象时不调配置回调，
// 以 plugins.process.failed 失败并进入 Failed。
func (s *service) Configure(ctx context.Context, request *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	r := s.r
	if !r.lifecycle.claim(stepConfigure, primitives.Resolved) {
		return nil, status.Error(codes.FailedPrecondition, "Configure is accepted once, after the handshake")
	}
	r.grantedMu.Lock()
	r.granted = slices.Clone(request.GrantedPermissions)
	r.grantedMu.Unlock()

	section := request.Configuration
	if section == "" {
		section = "{}"
	}
	// 先过 RFC-0001 的共同 JSON 读取检查（语法、重复成员等），再看根是不是对象
	if root, problem := jsontext.Parse([]byte(section)); problem != nil || root.Kind != jsontext.Object {
		failure := processFailed(r.manifest.ID, phaseConfigure, reasonConfigurationNotObject)
		r.lifecycle.transition(primitives.Failed, failure)
		r.logger.Error("configure failed: the configuration is not a JSON object")
		return &pluginv1.ConfigureResponse{Status: ToStatus(primitives.Fail[primitives.Unit](failure))}, nil
	}
	configuration := Configuration{
		Section:            json.RawMessage(section),
		GrantedPermissions: slices.Clone(request.GrantedPermissions),
		// 绑定连同属性交给作者；主值与属性值可能是凭据，运行时不把它们写进日志与错误（lifecycle.md §7.2）
		Bindings:      bindingsOf(request.Bindings),
		WorkDirectory: request.WorkDirectory,
	}
	outcome := primitives.Success()
	if hook := r.opts.Hooks.Configure; hook != nil {
		outcome = r.runHook(phaseConfigure, func() error { return hook(ctx, configuration) })
	}
	if outcome.OK() {
		r.lifecycle.transition(primitives.Configured, nil)
	} else {
		r.lifecycle.transition(primitives.Failed, outcome.Err())
		r.logger.Error("configure failed", "code", outcome.Err().Code, "messageKey", outcome.Err().MessageKey)
	}
	return &pluginv1.ConfigureResponse{Status: ToStatus(outcome)}, nil
}

// bindingsOf 把 Configure 下发的绑定换成交给作者的绑定，保持先后（lifecycle.md §7.2，ADR-0285）：属性原样拷贝，
// 属性名不折叠大小写、不核对文法与上限，没有属性时是空表；主值为空照常交出。
func bindingsOf(delivered []*pluginv1.Binding) []Binding {
	if len(delivered) == 0 {
		return nil
	}
	bindings := make([]Binding, 0, len(delivered))
	for _, binding := range delivered {
		// 另建一张表而不是 maps.Clone：没有属性时也要是空表、不是 nil；拷贝之后作者改它碰不到请求消息
		properties := make(map[string]string, len(binding.GetProperties()))
		maps.Copy(properties, binding.GetProperties())
		bindings = append(bindings, Binding{Name: binding.GetName(), Type: binding.GetType(), Value: binding.GetValue(), Properties: properties})
	}
	return bindings
}

// Start 调启动回调（lifecycle.md §7.3），只在 Configured 接受一次：成功后进入 Started，有健康回调就立即求一次并按结果进入
// Ready、Degraded 或 Failed，没有就进入 Ready，然后才应答 ok；失败进入 Failed。
func (s *service) Start(ctx context.Context, _ *pluginv1.StartRequest) (*pluginv1.StartResponse, error) {
	r := s.r
	if !r.lifecycle.claim(stepStart, primitives.Configured) {
		return nil, status.Error(codes.FailedPrecondition, "Start is accepted once, after a successful Configure")
	}
	outcome := primitives.Success()
	if hook := r.opts.Hooks.Start; hook != nil {
		outcome = r.runHook(phaseStart, func() error { return hook(ctx) })
	}
	if !outcome.OK() {
		r.lifecycle.transition(primitives.Failed, outcome.Err())
		r.logger.Error("start failed", "code", outcome.Err().Code, "messageKey", outcome.Err().MessageKey)
		return &pluginv1.StartResponse{Status: ToStatus(outcome)}, nil
	}
	r.started.Store(true)
	r.lifecycle.transition(primitives.Started, nil)
	if r.opts.Hooks.Health != nil {
		r.checkHealth(true)
	} else {
		r.lifecycle.transition(primitives.Ready, nil)
	}
	state, _ := r.lifecycle.current()
	r.logger.Info("started", "state", state.String(), "operations", r.invoker.Registry().Len(), "subscriptions", len(r.subscriptions))
	return &pluginv1.StartResponse{Status: ToStatus(outcome)}, nil
}

// Stop 按 lifecycle.md §7.5 停止，只在 Declared、Draining、Stopped 之外接受一次：启动回调成功过的先进入 Draining（只从可服务的状态），
// 拒绝新调用、等在途调用到宽限（grace_seconds 为 0 按 5 秒），到时触发它们的取消信号、不再等，再调停止回调；然后应答，
// 结束 WatchHealth 流、关闭服务端，Serve 返回 nil（退出码 0）。启动回调没有成功过的不调停止回调，直接应答 ok。
func (s *service) Stop(ctx context.Context, request *pluginv1.StopRequest) (*pluginv1.StopResponse, error) {
	r := s.r
	if !r.lifecycle.claim(stepStop, stoppableStates...) {
		state, _ := r.lifecycle.current()
		return nil, status.Errorf(codes.FailedPrecondition, "Stop is not accepted in state %s", state)
	}
	grace := time.Duration(request.GraceSeconds) * time.Second
	if grace <= 0 {
		grace = defaultStopGrace
	}
	begun := time.Now()
	r.logger.Info("stopping", "reason", request.Reason, "grace", grace.String())
	r.drain(false, grace)
	outcome := primitives.Success()
	if r.started.Load() {
		outcome = r.stop(ctx, request.Reason, max(grace-time.Since(begun), 0))
	}
	// 应答之后再结束流、关闭服务端：GracefulStop 会等本次 RPC 把应答送出
	go r.finish(time.Now().Add(shutdownLimit))
	return &pluginv1.StopResponse{Status: ToStatus(outcome)}, nil
}

// Health 返回当前健康（lifecycle.md §7.4），任何状态都可调用：可服务期间每次求一次健康回调，其余状态报本地状态、没有健康项。
func (s *service) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return toProtoHealth(s.r.checkHealth(false)), nil
}

// WatchHealth 先推一次当前状态，之后状态、健康项或 error 变化时推送；可服务期间每 10 秒求一次健康回调；
// 进入 Draining 时推一次，应答 Stop 后结束流（lifecycle.md §7.4）。
func (s *service) WatchHealth(_ *pluginv1.HealthRequest, stream grpc.ServerStreamingServer[pluginv1.HealthResponse]) error {
	r := s.r
	changes, unwatch := r.lifecycle.watch()
	defer unwatch()
	last := r.checkHealth(false)
	if err := stream.Send(toProtoHealth(last)); err != nil {
		return err
	}
	ticker := time.NewTicker(r.healthInterval)
	defer ticker.Stop()
	for {
		ending := false
		select {
		case <-stream.Context().Done():
			return nil
		case <-r.ended:
			// 结束之前补推还没推出的变化：Draining 与结束几乎同时发生时 select 可能先选到这里
			ending = true
		case <-changes:
		case <-ticker.C:
			r.checkHealth(false)
		}
		if current := r.lifecycle.snapshot(); !reflect.DeepEqual(current, last) {
			last = current
			if err := stream.Send(toProtoHealth(current)); err != nil {
				return err
			}
		}
		if ending {
			return nil
		}
	}
}

// Invoke 调用一个一元操作，依次核对（invocation.md §1）：会话令牌（拦截器）；操作在声明里，否则 gRPC Unimplemented；
// 可服务，否则 PLUGINS_UNAVAILABLE_001 + plugins.process.unavailable；然后还原上下文，经调用器与完整管道执行，payload 为空等同 {}。
func (s *service) Invoke(ctx context.Context, request *pluginv1.InvokeRequest) (*pluginv1.InvokeResponse, error) {
	r := s.r
	if _, ok := r.invoker.Registry().Get(request.Operation); !ok {
		return nil, status.Errorf(codes.Unimplemented, "the operation '%s' is not provided by this plugin", request.Operation)
	}
	if state, _ := r.lifecycle.current(); !state.IsServing() {
		return &pluginv1.InvokeResponse{Status: ToStatus(r.unavailable(state))}, nil
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	invocationCtx, release := r.newContext(callCtx, request.Metadata)
	defer release()
	leave, ok := r.calls.enter(cancel, invocationCtx.Tenant())
	if !ok {
		// 核对可服务之后、登记之前停止已经开始：与停止之后到达的调用一样拒绝
		state, _ := r.lifecycle.current()
		return &pluginv1.InvokeResponse{Status: ToStatus(r.unavailable(state))}, nil
	}
	defer leave()
	response := r.invoker.Invoke(invocationCtx, operation.Request{ID: request.Operation, Payload: request.Payload})
	return &pluginv1.InvokeResponse{Status: ToStatus(response.Outcome), Payload: response.Payload}, nil
}

// InvokeStream 在流式操作契约的 RFC 之前不实现（invocation.md §1）：令牌之后读第一帧，流在第一帧之前结束或第一帧不是 start 时
// 以 gRPC InvalidArgument 结束，第一帧是 start 时以 gRPC Unimplemented 结束，不论操作是否声明、插件是否就绪。
func (s *service) InvokeStream(stream grpc.BidiStreamingServer[pluginv1.StreamFrame, pluginv1.StreamFrame]) error {
	frame, err := stream.Recv()
	switch {
	case errors.Is(err, io.EOF):
		return status.Error(codes.InvalidArgument, "the stream ended before its first frame")
	case err != nil:
		return err
	case frame.GetStart() == nil:
		return status.Error(codes.InvalidArgument, "the first frame of InvokeStream must be start")
	default:
		return status.Error(codes.Unimplemented, "plugins do not implement streaming operations before the streaming contract is specified")
	}
}

// HandleEvent 投递一条消息，依次核对（invocation.md §4）：会话令牌（拦截器）；有信封、方式是空、competing 或 broadcast，否则 gRPC
// InvalidArgument；按（主题，方式，类型）找订阅，没有即 KERNEL_NOTFOUND_001 + plugins.event.unsubscribed；可服务，否则
// plugins.process.unavailable；然后还原上下文，匹配的订阅按声明顺序依次交给各自的处理器，第一个失败即结果。
func (s *service) HandleEvent(ctx context.Context, request *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error) {
	r := s.r
	mode := SubscriptionMode(request.GetMode())
	if request.Envelope == nil || !isSubscriptionMode(mode) {
		return nil, status.Errorf(codes.InvalidArgument, "a delivery needs an envelope and the mode empty, '%s' or '%s'", SubscriptionModeCompeting, SubscriptionModeBroadcast)
	}
	envelope := FromProtoEnvelope(request.Envelope)
	handlers := r.handlersFor(envelope, mode)
	if len(handlers) == 0 {
		return &pluginv1.HandleEventResponse{Status: ToStatus(primitives.Fail[primitives.Unit](eventUnsubscribed(r.manifest.ID, envelope, mode)))}, nil
	}
	if state, _ := r.lifecycle.current(); !state.IsServing() {
		return &pluginv1.HandleEventResponse{Status: ToStatus(r.unavailable(state))}, nil
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	invocationCtx, release := r.newContext(callCtx, request.Metadata)
	defer release()
	leave, ok := r.calls.enter(cancel, invocationCtx.Tenant())
	if !ok {
		state, _ := r.lifecycle.current()
		return &pluginv1.HandleEventResponse{Status: ToStatus(r.unavailable(state))}, nil
	}
	defer leave()
	// 处理器的签名里没有投递的方式：放进上下文，处理器用 EventDeliveryOf 取；空方式同 competing
	invocationCtx.Set(deliveryKey{}, EventDelivery{Attempt: request.Attempt, Mode: mode.orCompeting()})
	for _, handler := range handlers {
		if outcome := r.deliver(invocationCtx, handler, envelope, request.Attempt); !outcome.OK() {
			return &pluginv1.HandleEventResponse{Status: ToStatus(outcome)}, nil
		}
	}
	return &pluginv1.HandleEventResponse{Status: ToStatus(primitives.Success())}, nil
}

// unavailable 创建不在可服务状态时的失败，state 是此刻的状态。
func (r *runtime) unavailable(state primitives.LifecycleState) primitives.Result[primitives.Unit] {
	return primitives.Fail[primitives.Unit](processUnavailable(r.manifest.ID, state))
}

// newContext 从 gRPC 上下文与协议元数据还原调用上下文（invocation.md §2，规则同 FromMetadata），并放入宿主客户端。
// 返回的函数在调用结束时调用：归还截止时间的定时器，不让每个已结束的调用各留一个到截止时间才到期的
func (r *runtime) newContext(parent context.Context, metadata *pluginv1.InvocationMetadata) (*invocation.Context, context.CancelFunc) {
	ctx, release := invocation.NewWithCancel(parent, metadataOptions(parent, metadata)...)
	ctx.Set(hostKey{}, r.host)
	return ctx, release
}

// handlersFor 按主题、投递的订阅方式与可选的消息类型找订阅处理器，按声明顺序（invocation.md §4）。
func (r *runtime) handlersFor(envelope Envelope, mode SubscriptionMode) []EventHandler {
	// 方式必须相同：同一主题的不同类型可以各用一种方式，一条消息以两种方式各到一次时要交给各自的处理器，不能改交给另一种方式的
	mode = mode.orCompeting()
	var handlers []EventHandler
	for _, subscription := range r.subscriptions {
		declared := subscription.Subscription
		if declared.Topic == envelope.Topic && declared.Mode.orCompeting() == mode && (declared.MessageType == "" || declared.MessageType == envelope.Type) {
			handlers = append(handlers, subscription.Handler)
		}
	}
	return handlers
}

// deliver 把消息交给一个处理器；处理器 panic 为 KERNEL_UNEXPECTED_001 + kernel.invocation.unhandled_exception（RFC-0011 规则 5），
// panic 的值与堆栈只写进 stderr 日志，值是 FrameworkError 时作为内部错误带上。
func (r *runtime) deliver(ctx *invocation.Context, handler EventHandler, envelope Envelope, attempt uint32) (outcome primitives.Result[primitives.Unit]) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		r.logger.Error("an event handler panicked", "topic", envelope.Topic, "id", envelope.ID, "traceId", ctx.TraceID(), "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
		outcome = primitives.Fail[primitives.Unit](operation.UnhandledException(recovered))
	}()
	return handler(ctx, envelope, attempt)
}

// runHook 执行一个生命周期回调并换成结果（lifecycle.md §7.2、§7.5、§7.6）：返回的错误是（或用 %w 包着）*primitives.Error 是回调返回失败，
// 取出它原样带出；返回别的 error 或 panic 是回调抛出，为 PLUGINS_UNEXPECTED_001 + plugins.process.failed，reason 是错误的简单类型名或 panic。
// 错误的文本、panic 的值与堆栈只写进 stderr 日志：文本可能含路径、连接串，不能随错误交给宿主
func (r *runtime) runHook(phase string, call func() error) (outcome primitives.Result[primitives.Unit]) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.logger.Error("a lifecycle hook panicked", "phase", phase, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			outcome = primitives.Fail[primitives.Unit](processFailed(r.manifest.ID, phase, reasonPanic))
		}
	}()
	err := call()
	if err == nil {
		return primitives.Success()
	}
	// 装着 nil 指针的 error 不是失败：返回 (*primitives.Error)(nil) 的回调本意是成功
	if direct, ok := err.(*primitives.Error); ok && direct == nil {
		return primitives.Success()
	}
	// errors.As 而不是类型断言：回调给自己的失败加一层上下文（fmt.Errorf("load settings: %w", err)）之后仍是它返回的失败。
	// 链里找到的是 nil 指针时不算，落到下面按抛出处理
	var typed *primitives.Error
	if errors.As(err, &typed) && typed != nil {
		return primitives.Fail[primitives.Unit](typed)
	}
	name := errorTypeName(err)
	r.logger.Error("a lifecycle hook failed", "phase", phase, "type", name, "error", err.Error())
	return primitives.Fail[primitives.Unit](processFailed(r.manifest.ID, phase, name))
}

// errorTypeName 返回错误的简单类型名（lifecycle.md §7.6）：动态类型去掉指针标记与包名，泛型去掉类型实参（实参带完整的包路径），
// 如 *fs.PathError 写 PathError；没有名字的类型（嵌入 error 的匿名结构体）写它的种类。
func errorTypeName(err error) string {
	t := reflect.TypeOf(err)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := t.Name()
	if index := strings.IndexByte(name, '['); index >= 0 {
		name = name[:index]
	}
	if name == "" {
		return t.Kind().String()
	}
	return name
}

// checkHealth 求一次健康并返回结果（lifecycle.md §7.3、§7.4）。可服务期间（starting 时也在 Started）有健康回调就调用它：
// 健康项名不在清单 contributes.health 里的丢掉；回调 panic 时这一次是 Degraded，error 为 plugins.process.failed（phase 为 health）、
// 没有健康项。starting 时回调报 Ready、Degraded、Failed 之外的状态按 Ready：启动回调已经成功、健康回调没有报问题。
// 其余状态不调回调，报本地状态。
func (r *runtime) checkHealth(starting bool) Health {
	state, _ := r.lifecycle.current()
	if r.opts.Hooks.Health == nil || !(state.IsServing() || (starting && state == primitives.Started)) {
		return r.lifecycle.snapshot()
	}
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	reported, failure := r.callHealthHook()
	if failure != nil {
		return r.lifecycle.report(Health{State: primitives.Degraded, Error: failure}, starting)
	}
	health := Health{State: reported.State, Items: r.declaredItems(reported.Items), Error: reported.Error}
	if starting && health.State != primitives.Ready && health.State != primitives.Degraded && health.State != primitives.Failed {
		health.State = primitives.Ready
	}
	return r.lifecycle.report(health, starting)
}

// callHealthHook 调健康回调；panic 时返回 plugins.process.failed，值与堆栈只写进日志。
func (r *runtime) callHealthHook() (reported Health, failure *primitives.Error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.logger.Error("the health hook panicked", "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			failure = processFailed(r.manifest.ID, phaseHealth, reasonPanic)
		}
	}()
	return r.opts.Hooks.Health(), nil
}

// declaredItems 去掉清单 contributes.health 里没有的健康项；没有剩下的为 nil。
func (r *runtime) declaredItems(items []HealthItem) []HealthItem {
	declared := r.manifest.Health()
	var kept []HealthItem
	for _, item := range items {
		if slices.Contains(declared, item.Name) {
			kept = append(kept, item)
			continue
		}
		r.warnedMu.Lock()
		// 同一个名字在进程里只记一条告警（lifecycle.md §7.4）；记过的名字有上限，超出之后不再告警，集合不随回调报的名字无界增长
		first := !r.warned[item.Name] && len(r.warned) < maxWarnedHealthItems
		if first {
			r.warned[item.Name] = true
		}
		r.warnedMu.Unlock()
		if first {
			r.logger.Warn("health item is not declared in the manifest", "item", item.Name)
		}
	}
	return kept
}

// grantedPermissions 返回 Configure 下发的 granted_permissions；Configure 之前为空。
func (r *runtime) grantedPermissions() []string {
	r.grantedMu.RLock()
	defer r.grantedMu.RUnlock()
	return r.granted
}

// drain 开始停止：不再登记新的调用，然后等在途调用。cancelNow 为假是 Stop（lifecycle.md §7.5）：Started、Ready、Degraded 进入
// Draining，等到 wait，到时仍没结束的触发取消信号、不再等。cancelNow 为真是 stdin 关闭与 SIGTERM（§7.7）：只有可服务的（Ready、
// Degraded）进入 Draining，先触发全部取消信号再等到 wait。其余状态不迁移。
func (r *runtime) drain(cancelNow bool, wait time.Duration) {
	from := []primitives.LifecycleState{primitives.Started, primitives.Ready, primitives.Degraded}
	if cancelNow {
		from = []primitives.LifecycleState{primitives.Ready, primitives.Degraded}
	}
	r.lifecycle.transitionFrom(primitives.Draining, nil, from...)
	r.calls.close()
	if cancelNow {
		r.calls.cancelAll()
	}
	if !r.calls.wait(wait) && !cancelNow {
		r.logger.Warn("in-flight calls did not finish within the grace period; their cancellation is signalled")
		r.calls.cancelAll()
	}
}

// stop 调停止回调，在进程里最多一次；并发的 Stop 与收尾共用那一次的结果。失败只记录，进程照常退出（lifecycle.md §7.5）。
func (r *runtime) stop(ctx context.Context, reason string, grace time.Duration) primitives.Result[primitives.Unit] {
	r.stopOnce.Do(func() {
		r.stopOutcome = primitives.Success()
		if hook := r.opts.Hooks.Stop; hook != nil {
			r.stopOutcome = r.runHook(phaseStop, func() error { return hook(ctx, reason, grace) })
		}
		if !r.stopOutcome.OK() {
			r.logger.Error("stop failed", "code", r.stopOutcome.Err().Code, "messageKey", r.stopOutcome.Err().MessageKey)
		}
	})
	return r.stopOutcome
}

// shutdown 处理 stdin 关闭、SIGTERM 与 Serve 的上下文结束（lifecycle.md §7.7），只执行一次：可服务的进入 Draining，立即触发全部在途调用的
// 取消信号并等它们；启动回调成功过、停止回调还没调过的调一次（reason 为 shutdown）。从收到起最多 3 秒，到时不论是否完成都关闭服务端，
// Serve 返回 nil（退出码 0）；正在处理的 Stop 同样受这 3 秒限制。
func (r *runtime) shutdown(trigger string) {
	r.shutdownOnce.Do(func() {
		select {
		case <-r.stopped:
			// Stop 已经让服务端关闭，没有什么要收尾的
			return
		default:
		}
		deadline := time.Now().Add(shutdownLimit)
		r.logger.Info("shutting down", "trigger", trigger)
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.drain(true, time.Until(deadline))
			if r.started.Load() {
				ctx, cancel := context.WithDeadline(context.Background(), deadline)
				defer cancel()
				r.stop(ctx, "shutdown", max(time.Until(deadline), 0))
			}
		}()
		timer := time.NewTimer(max(time.Until(deadline), 0))
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			r.logger.Warn("the shutdown did not complete within its time limit; exiting")
		}
		r.finish(deadline)
	})
}

// finish 结束 WatchHealth 流并关闭服务端，只执行一次：GracefulStop 等仍在执行的 RPC（含刚应答的 Stop），最长到 deadline，
// 到时强行关闭；然后从 Draining 进入 Stopped，让 Serve 返回。
func (r *runtime) finish(deadline time.Time) {
	r.finishOnce.Do(func() {
		close(r.ended)
		done := make(chan struct{})
		go func() {
			r.server.GracefulStop()
			close(done)
		}()
		timer := time.NewTimer(max(time.Until(deadline), 0))
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			// 不理会取消信号的处理器拖住了 GracefulStop：只经令牌通知、不强行中止它们（RFC-0003），关闭连接后照样退出。
			// 不等 Stop 返回：连接都断了之后 GracefulStop 持着服务端的锁等处理器结束，这时的 Stop 会一直等那把锁
			go r.server.Stop()
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
