package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"google.golang.org/grpc"

	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/manifest"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// utf8BOM 是 UTF-8 的字节顺序标记。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Serve 以进程外插件的身份运行：核对清单与声明、读环境变量、选回调端点、监听、写握手行，然后服务到 Stop 完成、stdin 读到结尾、
// 收到 SIGTERM 或 ctx 结束（后三者按 lifecycle.md §7.7 收尾，最多 3 秒）；这些情况都返回 nil，进程以退出码 0 退出。
// 清单与声明不合（handshake.md §1 末段）或环境变量缺失时不监听、不写握手行，在 stderr 记一条 error 日志并返回 FrameworkError
// （清单读不出时是 manifest.Read 的 *primitives.Error）。XIHAN_PLUGIN_HOST_ADDRESS 缺失、不合法或没有能连的项时照常服务，
// 记一条 warning，回调以 plugins.host.unavailable 失败。
func Serve(ctx context.Context, opts Options) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Operations == nil {
		opts.Operations = operation.NewRegistry()
	}
	logger := slog.New(NewLogHandler(opts.Stderr, slog.LevelInfo))
	refuse := func(err error) error {
		logger.Error("the plugin refused to start", "error", err.Error())
		return err
	}

	// 清单文件的原文去掉开头的 BOM（handshake.md §3 第 3 条）：握手发的就是这份文本，读取也按它
	manifestText := bytes.TrimPrefix(opts.Manifest, utf8BOM)
	m, err := manifest.Read(manifestText)
	if err != nil {
		return refuse(err)
	}
	logger = logger.With("plugin", m.ID)
	if err := validate(m, opts); err != nil {
		return refuse(err)
	}

	versions, err := parseVersions(opts.Getenv(EnvProtocolVersions))
	if err != nil {
		return refuse(err)
	}
	session := opts.Getenv(EnvSession)
	if session == "" {
		return refuse(configurationError("%s is required", EnvSession))
	}
	invoker, err := operation.NewInvoker(opts.Operations, opts.Pipeline)
	if err != nil {
		return refuse(err)
	}

	r := &runtime{
		opts:           opts,
		manifest:       m,
		manifestText:   string(manifestText),
		invoker:        invoker,
		subscriptions:  opts.Subscriptions,
		lifecycle:      newLifecycle(),
		calls:          newCalls(),
		logger:         logger,
		session:        session,
		instance:       opts.Getenv(EnvInstance),
		locale:         opts.Getenv(EnvLocale),
		hostVersions:   versions,
		healthInterval: healthInterval,
		warned:         map[string]bool{},
		ended:          make(chan struct{}),
		stopped:        make(chan struct{}),
	}
	if primitivesText := opts.Getenv(EnvPrimitives); primitivesText != "" && primitivesText != strconv.Itoa(int(m.Primitives)) {
		logger.Warn("the host's primitives version differs from the manifest; the handshake will be rejected by the host", "host", primitivesText, "plugin", int(m.Primitives))
	}
	r.host = newHost(r, opts.Getenv(EnvHostAddress))
	defer r.host.Close()

	listener, endpoint, cleanup, err := listen(opts.Network, opts.Address)
	if err != nil {
		return refuse(err)
	}
	// 每条返回路径上服务都已停止、监听已关闭，此时才删私有目录
	defer cleanup()
	line := handshakeLine(endpoint)
	if _, ok := rules.ParseHandshakeLine(line); !ok {
		_ = listener.Close()
		return refuse(fmt.Errorf("plugin: the handshake line %q is not valid", line))
	}
	// 每次 RPC（含 Handshake）先核对会话令牌再进入服务实现，令牌不对的调用碰不到次序、生命周期与操作（handshake.md §4）
	guard := newSessionGuard(session)
	r.server = grpc.NewServer(grpc.ChainUnaryInterceptor(guard.unary), grpc.ChainStreamInterceptor(guard.stream))
	pluginv1.RegisterPluginServer(r.server, &service{r: r})
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- r.server.Serve(listener) }()

	// 握手行是 stdout 的第一行也是唯一一行（handshake.md §2）
	if _, err := fmt.Fprintln(opts.Stdout, line); err != nil {
		r.server.Stop()
		return fmt.Errorf("plugin: write handshake line: %w", err)
	}
	r.logger.Info("listening", "endpoint", endpoint.String(), "instance", r.instance, "locale", r.locale)

	// stdin 读到结尾（或读出错）与 SIGTERM 都是收尾请求，不留孤儿进程（handshake.md §1、lifecycle.md §7.7）
	go func() {
		_, _ = io.Copy(io.Discard, opts.Stdin)
		r.shutdown("stdin closed")
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			r.shutdown("SIGTERM")
		case <-ctx.Done():
			r.shutdown("context done")
		case <-r.stopped:
		}
	}()

	select {
	case <-r.stopped:
	case err := <-serveErrors:
		if err != nil {
			r.server.Stop()
			return fmt.Errorf("plugin: serve: %w", err)
		}
		// Serve 只在 GracefulStop 或 Stop 之后返回 nil，等 finish 走完
		<-r.stopped
	}
	return nil
}

// validate 在监听与写握手行之前核对清单与声明（handshake.md §1 末段）：kind 是 plugin、isolation 是 out-of-process、没有
// contributes.pipeline、primitives 等于本 SDK 支持的原语层大版本、声明的操作都在 contributes.operations 里（登记表保证不重复）、
// 订阅的主题合文法、方式合法且（主题，类型）不重复、消息目录含 en-US。健康项由健康回调在运行时给出，名字不在 contributes.health 里的
// 在那时丢掉（lifecycle.md §7.4）。
func validate(m *manifest.Manifest, opts Options) error {
	if m.Kind != manifest.KindPlugin || m.Isolation != manifest.IsolationOutOfProcess {
		return configurationError("the manifest '%s' must describe a plugin with isolation out-of-process", m.ID)
	}
	// 只有 Wasm 插件能贡献中间件（RFC-0017「范围」，RFC-0002）：.NET 宿主在发现时以 PLUGINS_VALIDATION_001 拒绝写了 contributes.pipeline 的
	// 进程外插件，manifest.Read 照 .NET 的 ManifestReader 不查这一条，这里在监听前拒绝，不让作者以为中间件生效了
	if m.Contributes != nil && len(m.Contributes.Pipeline) > 0 {
		return configurationError("the plugin '%s' must not declare 'contributes.pipeline': only wasm plugins can contribute middleware", m.ID)
	}
	if version := strconv.Itoa(int(m.Primitives)); version != primitives.SpecVersion {
		return configurationError("the manifest '%s' declares primitives %s, but this SDK supports primitives %s", m.ID, version, primitives.SpecVersion)
	}
	for _, descriptor := range opts.Operations.List() {
		if !slices.Contains(m.Operations(), descriptor.Contract.ID) {
			return configurationError("the operation '%s' is not declared in contributes.operations of '%s'", descriptor.Contract.ID, m.ID)
		}
	}
	// 订阅按（主题，类型）对去重而不看方式：宿主拒绝同一对以两种方式各声明一次（handshake.md §3 第 5 条），在监听前就拒绝
	declared := make(map[[2]string]bool, len(opts.Subscriptions))
	for _, subscription := range opts.Subscriptions {
		declaration := subscription.Subscription
		if subscription.Handler == nil || !isTopic(declaration.Topic) {
			return configurationError("the subscription '%s' of '%s' needs a valid topic and a handler", declaration.Topic, m.ID)
		}
		if !isSubscriptionMode(declaration.Mode) {
			return configurationError("the subscription '%s' of '%s' has the mode '%s'; the mode must be empty, '%s' or '%s'", declaration.Topic, m.ID, declaration.Mode, SubscriptionModeCompeting, SubscriptionModeBroadcast)
		}
		key := [2]string{declaration.Topic, declaration.MessageType}
		if declared[key] {
			return configurationError("the subscription '%s' with the message type '%s' is declared more than once by '%s'", declaration.Topic, declaration.MessageType, m.ID)
		}
		declared[key] = true
	}
	for _, catalog := range opts.Catalogs {
		if _, ok := catalog.Locales["en-US"]; !ok {
			return configurationError("the message catalog '%s' of '%s' has no en-US templates", catalog.Name, m.ID)
		}
	}
	return nil
}

// newHost 按 XIHAN_PLUGIN_HOST_ADDRESS 选回调端点并创建宿主客户端（handshake.md §4）；没有能连的端点时客户端照样创建，
// 回调一律以 plugins.host.unavailable 失败，启动时记一条 warning。
func newHost(r *runtime, value string) *Host {
	host := &Host{pluginID: r.manifest.ID, declared: slices.Clone(r.manifest.Permissions), granted: r.grantedPermissions, inFlight: r.calls.hasTenant, session: r.session}
	endpoint, ok := selectHostEndpoint(value, callbackNetworks)
	if !ok {
		r.logger.Warn("no supported callback endpoint; callbacks to the host will fail", "variable", EnvHostAddress, "value", value)
		return host
	}
	conn, err := dial(endpoint)
	if err != nil {
		r.logger.Warn("the callback endpoint cannot be used; callbacks to the host will fail", "endpoint", endpoint.String(), "error", err.Error())
		return host
	}
	host.endpoint = endpoint
	host.conn = conn
	host.client = pluginv1.NewHostClient(conn)
	return host
}

// parseVersions 解析宿主支持的协议版本列表（逗号分隔、升序）。
func parseVersions(text string) ([]uint32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, configurationError("%s is required", EnvProtocolVersions)
	}
	var versions []uint32
	for _, part := range strings.Split(text, ",") {
		value, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		if err != nil {
			return nil, configurationError("%s must be a comma-separated list of versions, got %q", EnvProtocolVersions, text)
		}
		versions = append(versions, uint32(value))
	}
	return versions, nil
}

// isTopic 判断主题是否符合 RFC-0009 的文法。
func isTopic(topic string) bool {
	if len(topic) == 0 || len(topic) > 128 {
		return false
	}
	for index, char := range topic {
		lower := char >= 'a' && char <= 'z'
		digit := char >= '0' && char <= '9'
		if index == 0 && !lower && !digit {
			return false
		}
		if !lower && !digit && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

// isSubscriptionMode 判断订阅方式是否为空、competing 或 broadcast（RFC-0009「订阅方式」）。
func isSubscriptionMode(mode SubscriptionMode) bool {
	return mode == "" || mode == SubscriptionModeCompeting || mode == SubscriptionModeBroadcast
}

// configurationError 创建配置错误。
func configurationError(format string, arguments ...any) error {
	return &primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: fmt.Sprintf(format, arguments...) + "."}
}
