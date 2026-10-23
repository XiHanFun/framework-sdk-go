package plugin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/xihanfun/framework-sdk-go/manifest"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// Serve 以进程外插件的身份运行：校验清单与声明、读环境变量、监听、写握手行，然后服务到 Stop 完成或 stdin 关闭；
// 正常停止返回 nil。配置错误（清单不合法、声明的操作不在清单里、环境变量缺失）返回 FrameworkError。
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
	if opts.StopGrace <= 0 {
		opts.StopGrace = 5 * time.Second
	}
	if opts.Operations == nil {
		opts.Operations = operation.NewRegistry()
	}

	m, err := manifest.Read(opts.Manifest)
	if err != nil {
		return err
	}
	if m.Kind != manifest.KindPlugin || m.Isolation != manifest.IsolationOutOfProcess {
		return configurationError("the manifest '%s' must describe a plugin with isolation out-of-process", m.ID)
	}
	for _, descriptor := range opts.Operations.List() {
		if !slices.Contains(m.Operations(), descriptor.Contract.ID) {
			return configurationError("the operation '%s' is not declared in contributes.operations of '%s'", descriptor.Contract.ID, m.ID)
		}
	}
	for _, subscription := range opts.Subscriptions {
		if subscription.Handler == nil || !isTopic(subscription.Subscription.Topic) {
			return configurationError("the subscription '%s' of '%s' needs a valid topic and a handler", subscription.Subscription.Topic, m.ID)
		}
	}
	for _, catalog := range opts.Catalogs {
		if _, ok := catalog.Locales["en-US"]; !ok {
			return configurationError("the message catalog '%s' of '%s' has no en-US templates", catalog.Name, m.ID)
		}
	}

	versions, err := parseVersions(opts.Getenv(EnvProtocolVersions))
	if err != nil {
		return err
	}
	session := opts.Getenv(EnvSession)
	if session == "" {
		return configurationError("%s is required", EnvSession)
	}
	invoker, err := operation.NewInvoker(opts.Operations, opts.Pipeline)
	if err != nil {
		return err
	}

	r := &runtime{
		opts:          opts,
		manifest:      m,
		invoker:       invoker,
		subscriptions: opts.Subscriptions,
		lifecycle:     newLifecycle(),
		logger:        slog.New(NewLogHandler(opts.Stderr, slog.LevelInfo)).With("plugin", m.ID),
		session:       session,
		instance:      opts.Getenv(EnvInstance),
		locale:        opts.Getenv(EnvLocale),
		hostVersions:  versions,
		stopped:       make(chan struct{}),
	}
	if address := opts.Getenv(EnvHostAddress); address != "" {
		endpoint, ok := ParseEndpoint(address)
		if !ok {
			return configurationError("%s must be <network> <address>, got %q", EnvHostAddress, address)
		}
		host, err := newHost(endpoint, session)
		if err != nil {
			return err
		}
		r.host = host
		defer host.Close()
	}
	if primitivesText := opts.Getenv(EnvPrimitives); primitivesText != "" && primitivesText != strconv.Itoa(int(m.Primitives)) {
		r.logger.Warn("the host's primitives version differs from the manifest; the handshake will be rejected by the host", "host", primitivesText, "plugin", int(m.Primitives))
	}

	listener, endpoint, err := listen(opts.Network, opts.Address)
	if err != nil {
		return err
	}
	r.server = grpc.NewServer()
	pluginv1.RegisterPluginServer(r.server, &service{r: r})
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- r.server.Serve(listener) }()

	// 握手行是 stdout 的第一行也是唯一一行（handshake.md §2）
	if _, err := fmt.Fprintf(opts.Stdout, "%s %s %s\n", HandshakeLineVersion, endpoint.Network, endpoint.Address); err != nil {
		r.server.Stop()
		return fmt.Errorf("plugin: write handshake line: %w", err)
	}
	r.logger.Info("listening", "endpoint", endpoint.String(), "instance", r.instance, "locale", r.locale)

	// stdin 关闭即退出，不留孤儿进程（handshake.md §1）
	go func() {
		_, _ = io.Copy(io.Discard, opts.Stdin)
		r.logger.Info("stdin closed; stopping")
		r.shutdown(opts.StopGrace)
	}()

	select {
	case <-r.stopped:
	case <-ctx.Done():
		r.shutdown(opts.StopGrace)
		<-r.stopped
	case err := <-serveErrors:
		if err != nil {
			return fmt.Errorf("plugin: serve: %w", err)
		}
	}
	return nil
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

// configurationError 创建配置错误。
func configurationError(format string, arguments ...any) error {
	return &primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: fmt.Sprintf(format, arguments...) + "."}
}
