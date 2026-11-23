package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-spec/conformance"
	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// probeID 是测试插件的清单 ID。
const probeID = "xihan.samples.probe"

// testManifest 是测试插件的清单：两个操作、一个配置节、一个健康项，回调的两种权限都声明了。
const testManifest = `{
  "id": "xihan.samples.probe",
  "version": "0.1.0",
  "primitives": "1",
  "stability": "experimental",
  "kind": "plugin",
  "isolation": "out-of-process",
  "contributes": { "operations": ["echoes.create", "probes.run"], "config": { "section": "XiHan:Echo" }, "health": ["echo"] },
  "permissions": ["capability:kernel.pipeline", "capability:messaging.queue"],
  "entry": "probe"
}`

// allPermissions 是测试插件声明的全部权限，Configure 时默认全部授予。
var allPermissions = []string{"capability:kernel.pipeline", "capability:messaging.queue"}

// echoRequest 是 echoes.create 的请求。
type echoRequest struct {
	Text string `json:"text"`
}

// echoResponse 是 echoes.create 的响应。
type echoResponse struct {
	Text        string `json:"text"`
	Correlation string `json:"correlation"`
}

// harness 在进程内运行 Serve 并以 gRPC 客户端连上它。
type harness struct {
	t        *testing.T
	client   pluginv1.PluginClient
	endpoint Endpoint
	line     string
	stdin    *io.PipeWriter
	done     chan error
	stderr   *syncBuffer
	seen     *recorder
}

// recorder 记录回调；测试可以在启动前放入 probes.run 执行的函数。
type recorder struct {
	mu         sync.Mutex
	configured Configuration
	started    bool
	stops      []string
	healths    int
	events     []string
	// run 是 probes.run 执行的函数；为空时 probes.run 直接成功
	run func(ctx *invocation.Context) *primitives.Error
}

// record 在锁里改记录。
func (r *recorder) record(change func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change()
}

// read 在锁里读记录。
func read[T any](r *recorder, get func() T) T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return get()
}

// syncBuffer 是可以并发写、随时读的缓冲：运行时在服务端的 goroutine 里写日志。
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

// Write 实现 io.Writer。
func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

// String 返回已写的内容。
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// probeOptions 组装测试插件的选项：echoes.create 回声、probes.run 执行 seen.run；同一主题一个竞争订阅与一个只收 probe.cleared 的广播订阅；
// 生命周期回调记进 seen，健康回调报 Ready 与一个声明的、一个未声明的健康项。
func probeOptions(t *testing.T, seen *recorder) Options {
	t.Helper()
	registry := operation.NewRegistry()
	echo := operation.HandlerFunc[echoRequest, echoResponse](func(ctx *invocation.Context, request echoRequest) primitives.Result[echoResponse] {
		if request.Text == "" {
			return primitives.Fail[echoResponse](primitives.Validation("PROBE_VALIDATION_001", "probe.text.missing", nil))
		}
		return primitives.Ok(echoResponse{Text: request.Text, Correlation: ctx.Correlation()})
	})
	if err := operation.Add(registry, operation.Contract{ID: "echoes.create", Stability: primitives.Experimental, Expose: []string{operation.ExposeHTTP}}, echo, nil, ""); err != nil {
		t.Fatal(err)
	}
	run := operation.HandlerFunc[struct{}, struct{}](func(ctx *invocation.Context, _ struct{}) primitives.Result[struct{}] {
		seen.mu.Lock()
		call := seen.run
		seen.mu.Unlock()
		if call != nil {
			if err := call(ctx); err != nil {
				return primitives.Fail[struct{}](err)
			}
		}
		return primitives.Ok(struct{}{})
	})
	if err := operation.Add(registry, operation.Contract{ID: "probes.run", Stability: primitives.Experimental}, run, nil, ""); err != nil {
		t.Fatal(err)
	}
	// 每个处理器记下自己的名字与信封 ID，断言能看出投递交给了哪个处理器
	handler := func(name string) EventHandler {
		return func(_ *invocation.Context, envelope Envelope, _ uint32) primitives.Result[primitives.Unit] {
			seen.record(func() { seen.events = append(seen.events, name+":"+envelope.ID) })
			return primitives.Success()
		}
	}
	return Options{
		Manifest:   []byte(testManifest),
		Operations: registry,
		// 同一主题：不限类型的竞争订阅，加上只收 probe.cleared 的广播订阅（invocation.md §4 的例子）
		Subscriptions: []SubscriptionHandler{
			{Subscription: Subscription{Topic: "probe.events"}, Handler: handler("competing")},
			{Subscription: Subscription{Topic: "probe.events", MessageType: "probe.cleared", Mode: SubscriptionModeBroadcast}, Handler: handler("broadcast")},
		},
		Catalogs: []Catalog{{Name: "probe", Locales: map[string]map[string]string{"en-US": {"probe.text.missing": "The text is required."}}}},
		Hooks: Hooks{
			Configure: func(_ context.Context, configuration Configuration) error {
				seen.record(func() { seen.configured = configuration })
				return nil
			},
			Start: func(context.Context) error { seen.record(func() { seen.started = true }); return nil },
			Stop: func(_ context.Context, reason string, _ time.Duration) error {
				seen.record(func() { seen.stops = append(seen.stops, reason) })
				return nil
			},
			Health: func() Health {
				seen.record(func() { seen.healths++ })
				return Health{State: primitives.Ready, Items: []HealthItem{{Name: "echo", Status: Healthy}, {Name: "undeclared", Status: Healthy}}}
			},
		},
	}
}

// start 以缺省的选项启动测试插件。
func start(t *testing.T, env map[string]string) *harness {
	t.Helper()
	return launch(t, env, nil)
}

// launch 启动测试插件：mutate 可在启动前改选项（含 seen.run）；读到握手行后以带会话令牌的客户端连上。
func launch(t *testing.T, env map[string]string, mutate func(opts *Options, seen *recorder)) *harness {
	t.Helper()
	seen := &recorder{}
	opts := probeOptions(t, seen)
	if mutate != nil {
		mutate(&opts, seen)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderr := &syncBuffer{}
	opts.Stdin = stdinReader
	opts.Stdout = stdoutWriter
	opts.Stderr = stderr
	opts.Getenv = func(key string) string { return env[key] }
	done := make(chan error, 1)
	go func() {
		err := Serve(context.Background(), opts)
		_ = stdoutWriter.Close()
		done <- err
	}()

	line, err := bufio.NewReader(stdoutReader).ReadString('\n')
	if err != nil {
		t.Fatalf("handshake line: %v (serve: %v)\n%s", err, <-done, stderr.String())
	}
	go func() { _, _ = io.Copy(io.Discard, stdoutReader) }()
	line = strings.TrimSuffix(line, "\n")
	parsed, ok := rules.ParseHandshakeLine(line)
	if !ok {
		t.Fatalf("the handshake line %q is not valid", line)
	}
	endpoint := Endpoint{Network: Network(parsed.Network), Address: parsed.Address}
	conn, err := dial(endpoint, withSession(env[EnvSession])...)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, client: pluginv1.NewPluginClient(conn), endpoint: endpoint, line: line, stdin: stdinWriter, done: done, stderr: stderr, seen: seen}
	t.Cleanup(func() {
		_ = conn.Close()
		// 测试没有让插件退出时关闭 stdin 收尾，等 Serve 返回，不让它的 goroutine 漏到下一个测试
		_ = stdinWriter.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after stdin closed")
		}
	})
	return h
}

// exited 等 Serve 返回，返回它的错误；within 之内没有返回即失败。
func (h *harness) exited(within time.Duration) error {
	h.t.Helper()
	select {
	case err := <-h.done:
		// 放回去，Cleanup 还要再读一次
		h.done <- err
		return err
	case <-time.After(within):
		h.t.Fatalf("Serve did not return within %s", within)
		return nil
	}
}

// withSession 让连接的每次调用都像宿主一样在元数据里带会话令牌；values 为空时不带。
func withSession(values ...string) []grpc.DialOption {
	var pairs []string
	for _, value := range values {
		pairs = append(pairs, SessionMetadataKey, value)
	}
	if len(pairs) == 0 {
		return nil
	}
	return []grpc.DialOption{
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, conn *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
			return invoker(metadata.AppendToOutgoingContext(ctx, pairs...), method, request, reply, conn, options...)
		}),
		grpc.WithStreamInterceptor(func(ctx context.Context, description *grpc.StreamDesc, conn *grpc.ClientConn, method string, streamer grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(metadata.AppendToOutgoingContext(ctx, pairs...), description, conn, method, options...)
		}),
	}
}

// defaultEnv 是宿主下发的环境变量，没有回调端点。
func defaultEnv() map[string]string {
	return map[string]string{EnvProtocolVersions: "1", EnvPrimitives: "1", EnvSession: "session-token", EnvInstance: "probe#1", EnvLocale: "en-US"}
}

// deadline 给测试的调用一个 10 秒的截止时间。
func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// handshake 以宿主的请求握手，必须成功。
func (h *harness) handshake(ctx context.Context) *pluginv1.HandshakeResponse {
	h.t.Helper()
	response, err := h.client.Handshake(ctx, &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{1}, Primitives: 1, InstanceId: "probe", DefaultLocale: "en-US"})
	if err != nil {
		h.t.Fatalf("handshake: %v", err)
	}
	return response
}

// configure 以给定的配置节与权限（nil 为全部）配置，必须得到应答。
func (h *harness) configure(ctx context.Context, section string, granted []string) *pluginv1.Status {
	h.t.Helper()
	if granted == nil {
		granted = allPermissions
	}
	response, err := h.client.Configure(ctx, &pluginv1.ConfigureRequest{Configuration: section, GrantedPermissions: granted, WorkDirectory: h.t.TempDir()})
	if err != nil {
		h.t.Fatalf("configure: %v", err)
	}
	return response.Status
}

// ready 握手、配置（权限 nil 为全部）并启动，三步都必须成功。
func (h *harness) ready(ctx context.Context, granted []string) {
	h.t.Helper()
	h.handshake(ctx)
	if configured := h.configure(ctx, `{"greeting":"hi"}`, granted); !configured.Ok {
		h.t.Fatalf("configure: %v", configured)
	}
	started, err := h.client.Start(ctx, &pluginv1.StartRequest{})
	if err != nil || !started.Status.Ok {
		h.t.Fatalf("start: %v %v", started, err)
	}
}

// health 求一次 Health，必须成功。
func (h *harness) health(ctx context.Context) *pluginv1.HealthResponse {
	h.t.Helper()
	response, err := h.client.Health(ctx, &pluginv1.HealthRequest{})
	if err != nil {
		h.t.Fatalf("health: %v", err)
	}
	return response
}

// expectStatus 核对 Status 是带消息键的失败（invocation.md §3）：码与键相等、text 为空、details 按 JSON 值等于 details（nil 表示没有）。
func expectStatus(t *testing.T, what string, s *pluginv1.Status, code, key string, details map[string]any) {
	t.Helper()
	if s == nil || s.Ok || s.Error == nil {
		t.Errorf("%s must fail with %s %s, got %v", what, code, key, s)
		return
	}
	if s.Error.Code != code || s.Error.MessageKey != key {
		t.Errorf("%s must fail with %s %s, got %s %s", what, code, key, s.Error.Code, s.Error.MessageKey)
	}
	if s.Error.Text != "" {
		t.Errorf("%s: an error with a message key must leave text empty, got %q", what, s.Error.Text)
	}
	if details == nil {
		if s.Error.Details != "" {
			t.Errorf("%s must have no details, got %s", what, s.Error.Details)
		}
		return
	}
	var actual map[string]any
	if err := json.Unmarshal([]byte(s.Error.Details), &actual); err != nil || !reflect.DeepEqual(actual, details) {
		t.Errorf("%s: details %s, want %v", what, s.Error.Details, details)
	}
}

// expectError 核对原语层错误的码、键与 details（nil 表示没有）。
func expectError(t *testing.T, what string, err *primitives.Error, code, key string, details map[string]any) {
	t.Helper()
	if err == nil {
		t.Errorf("%s must fail with %s %s, got no error", what, code, key)
		return
	}
	if err.Code != code || err.MessageKey != key {
		t.Errorf("%s must fail with %s %s, got %s %s (%v)", what, code, key, err.Code, err.MessageKey, err.Details)
		return
	}
	if len(details) == 0 && len(err.Details) == 0 {
		return
	}
	if !reflect.DeepEqual(err.Details, details) {
		t.Errorf("%s: details %v, want %v", what, err.Details, details)
	}
}

func TestServeRunsTheProtocol(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)

	// 声明的操作在 Start 之前报 plugins.process.unavailable，state 是此刻的状态
	early, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{"text":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "an invoke before the handshake", early.Status, ErrorCodeUnavailable, MessageUnavailable, map[string]any{"id": probeID, "state": "Declared"})

	handshake, err := h.client.Handshake(ctx, &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{1, 2}, Primitives: 1, InstanceId: "probe", DefaultLocale: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	if handshake.ProtocolVersion != 1 || handshake.Primitives != 1 || handshake.Manifest != testManifest || handshake.Sdk.Name != primitives.SDKName || handshake.Sdk.Language != "go" {
		t.Errorf("handshake %+v", handshake)
	}
	if len(handshake.Operations) != 2 || handshake.Operations[0].Id != "echoes.create" || handshake.Operations[0].Stability != "experimental" || handshake.Operations[0].Expose[0] != "http" {
		t.Errorf("operations %+v", handshake.Operations)
	}
	for _, declared := range handshake.Operations {
		if declared.Streaming {
			t.Errorf("operation %s is declared as streaming; plugins declare only unary operations", declared.Id)
		}
	}
	// 只有作者的目录，SDK 运行时的目录不随握手发送（handshake.md §3 第 6 条）
	if len(handshake.Catalogs) != 1 || handshake.Catalogs[0].Name != "probe" || handshake.Catalogs[0].Locales[0].Locale != "en-US" {
		t.Errorf("catalogs %+v", handshake.Catalogs)
	}
	// 订阅按声明的顺序，方式总是写出：没写方式的写 competing（handshake.md §3 第 5 条）
	if len(handshake.Subscriptions) != 2 ||
		handshake.Subscriptions[0].GetTopic() != "probe.events" || handshake.Subscriptions[0].GetMode() != "competing" ||
		handshake.Subscriptions[1].GetTopic() != "probe.events" || handshake.Subscriptions[1].GetMessageType() != "probe.cleared" || handshake.Subscriptions[1].GetMode() != "broadcast" {
		t.Errorf("subscriptions %+v", handshake.Subscriptions)
	}

	configured, err := h.client.Configure(ctx, &pluginv1.ConfigureRequest{Configuration: `{"greeting":"hi"}`, GrantedPermissions: allPermissions, WorkDirectory: t.TempDir(), Bindings: []*pluginv1.Binding{{Name: "cache", Type: "cache:distributed", Value: "redis://localhost"}}})
	if err != nil || !configured.Status.Ok {
		t.Fatalf("configure %v %v", configured, err)
	}
	seenConfiguration := read(h.seen, func() Configuration { return h.seen.configured })
	if string(seenConfiguration.Section) != `{"greeting":"hi"}` || len(seenConfiguration.Bindings) != 1 || seenConfiguration.Bindings[0].Value != "redis://localhost" {
		t.Errorf("configuration %+v", seenConfiguration)
	}
	started, err := h.client.Start(ctx, &pluginv1.StartRequest{})
	if err != nil || !started.Status.Ok || !read(h.seen, func() bool { return h.seen.started }) {
		t.Fatalf("start %v %v", started, err)
	}

	health := h.health(ctx)
	if health.State != pluginv1.LifecycleState_LIFECYCLE_STATE_READY || len(health.Items) != 1 || health.Items[0].Name != "echo" {
		t.Errorf("health %v", health)
	}
	watchCtx, cancelWatch := context.WithCancel(ctx)
	watch, err := h.client.WatchHealth(watchCtx, &pluginv1.HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if first, err := watch.Recv(); err != nil || first.State != pluginv1.LifecycleState_LIFECYCLE_STATE_READY {
		t.Errorf("watch %v %v", first, err)
	}
	cancelWatch()

	invoked, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{"text":"hello"}`), Metadata: &pluginv1.InvocationMetadata{TraceParent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", Baggage: map[string]string{"xihan.correlation": "c1"}}})
	if err != nil || !invoked.Status.Ok {
		t.Fatalf("invoke %v %v", invoked, err)
	}
	var response echoResponse
	if err := json.Unmarshal(invoked.Payload, &response); err != nil || response.Text != "hello" || response.Correlation != "c1" {
		t.Errorf("payload %s", invoked.Payload)
	}
	// 作者的业务失败带消息键，text 留空由宿主翻译
	failed, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "a business failure", failed.Status, "PROBE_VALIDATION_001", "probe.text.missing", nil)
	if _, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.missing"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("unknown operation must be Unimplemented, got %v", err)
	}

	// 停止时仍打开的 WatchHealth 流推一次 Draining，应答 Stop 后结束
	openWatch, err := h.client.WatchHealth(ctx, &pluginv1.HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openWatch.Recv(); err != nil {
		t.Fatal(err)
	}
	stopping := time.Now()
	stopped, err := h.client.Stop(ctx, &pluginv1.StopRequest{GraceSeconds: 5, Reason: "unload"})
	if err != nil || !stopped.Status.Ok {
		t.Fatalf("stop %v %v", stopped, err)
	}
	if stops := read(h.seen, func() []string { return h.seen.stops }); !reflect.DeepEqual(stops, []string{"unload"}) {
		t.Errorf("the stop hook must run once with the host's reason, got %v", stops)
	}
	sawDraining := false
	for {
		update, err := openWatch.Recv()
		if err != nil {
			break
		}
		if update.State != pluginv1.LifecycleState_LIFECYCLE_STATE_DRAINING {
			t.Errorf("unexpected health update after Stop: %v", update)
		}
		sawDraining = true
	}
	if !sawDraining {
		t.Error("WatchHealth must push Draining when the plugin stops")
	}
	if err := h.exited(5 * time.Second); err != nil {
		t.Errorf("serve returned %v", err)
	}
	if time.Since(stopping) > 2*time.Second {
		t.Errorf("Serve took %s to return after Stop; the open stream held the graceful stop", time.Since(stopping))
	}
	if !strings.Contains(h.stderr.String(), `"level":"information"`) || !strings.Contains(h.stderr.String(), `"message":"handshake completed"`) {
		t.Errorf("stderr %s", h.stderr.String())
	}
}

func TestServeListensInAPrivateSocketDirectory(t *testing.T) {
	h := start(t, defaultEnv())
	if h.endpoint.Network != NetworkUnix {
		t.Skipf("the temporary directory contains whitespace, the plugin listens on %s", h.endpoint.Network)
	}
	// 套接字在临时目录下新建的私有目录里，不直接放在共享的临时目录
	directory := filepath.Dir(h.endpoint.Address)
	if filepath.Clean(directory) == filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(directory), "xihan-plugin-") {
		t.Errorf("the socket %q must be in a private directory under the temporary directory", h.endpoint.Address)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		t.Fatalf("the socket directory: %v", err)
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("the socket directory must be 0700, got %o", info.Mode().Perm())
	}
	h.handshake(deadline(t))

	// 停止后私有目录随之删除
	_ = h.stdin.Close()
	_ = h.exited(5 * time.Second)
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Errorf("the socket directory must be removed after Serve returns, stat: %v", err)
	}
}

// TestServeWritesALoopbackTCPLine 核对 tcp 只写 127.0.0.1:<端口>，其他写法与含空白的地址在监听之前拒绝（handshake.md §2）。
func TestServeWritesALoopbackTCPLine(t *testing.T) {
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) { opts.Network = NetworkTCP })
	if h.endpoint.Network != NetworkTCP || !strings.HasPrefix(h.endpoint.Address, "127.0.0.1:") || !strings.HasPrefix(h.line, "xihan-plugin/1 tcp 127.0.0.1:") {
		t.Errorf("handshake line %q", h.line)
	}
	h.handshake(deadline(t))

	for _, bad := range []struct {
		network Network
		address string
	}{
		{NetworkTCP, "[::1]:0"},
		{NetworkTCP, "localhost:0"},
		{NetworkTCP, "0.0.0.0:0"},
		{NetworkUnix, filepath.Join(t.TempDir(), "with space.sock")},
		{NetworkUnix, filepath.Join(t.TempDir(), "nbsp\u00a0.sock")},
		{NetworkPipe, `\\.\pipe\xihan plugin`},
	} {
		var stdout bytes.Buffer
		var stderr syncBuffer
		env := defaultEnv()
		err := Serve(context.Background(), Options{Manifest: []byte(testManifest), Network: bad.network, Address: bad.address, Getenv: func(key string) string { return env[key] }, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")})
		if err == nil || stdout.Len() != 0 {
			t.Errorf("%s %q must be refused before the handshake line, got %v and %q", bad.network, bad.address, err, stdout.String())
		}
		if !strings.Contains(stderr.String(), `"level":"error"`) {
			t.Errorf("%s %q: the refusal must be logged as an error, got %s", bad.network, bad.address, stderr.String())
		}
	}
}

func TestServeRefusesWithoutACommonVersion(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)
	_, err := h.client.Handshake(ctx, &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{99}, Primitives: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %v", err)
	}
	// 协商失败什么都没做：之后有共同版本的握手照常成功
	h.handshake(ctx)
}

func TestServeRefusesCallsWithoutTheSessionToken(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)

	// 不带、带错、前缀、大小写不同、多带一个值（错的在前、对的在前、两个都对）：一元与流式调用都以 Unauthenticated 拒绝、不执行
	for _, presented := range [][]string{nil, {""}, {"session-toke"}, {"session-token2"}, {"SESSION-TOKEN"}, {"not-the-token", "session-token"}, {"session-token", "not-the-token"}, {"session-token", "session-token"}} {
		conn, err := dial(h.endpoint, withSession(presented...)...)
		if err != nil {
			t.Fatal(err)
		}
		client := pluginv1.NewPluginClient(conn)
		if _, err := client.Handshake(ctx, &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{1}, Primitives: 1}); status.Code(err) != codes.Unauthenticated {
			t.Errorf("handshake with %q: expected Unauthenticated, got %v", presented, err)
		}
		if _, err := client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{"text":"x"}`)}); status.Code(err) != codes.Unauthenticated {
			t.Errorf("invoke with %q: expected Unauthenticated, got %v", presented, err)
		}
		if _, err := client.Stop(ctx, &pluginv1.StopRequest{Reason: "intruder"}); status.Code(err) != codes.Unauthenticated {
			t.Errorf("stop with %q: expected Unauthenticated, got %v", presented, err)
		}
		watch, err := client.WatchHealth(ctx, &pluginv1.HealthRequest{})
		if err == nil {
			_, err = watch.Recv()
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("watch-health with %q: expected Unauthenticated, got %v", presented, err)
		}
		// 令牌先于读第一帧：不发帧也以 Unauthenticated 结束
		stream, err := client.InvokeStream(ctx)
		if err == nil {
			_, err = stream.Recv()
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("invoke-stream with %q: expected Unauthenticated, got %v", presented, err)
		}
		_ = conn.Close()
	}

	// 被拒的调用没有执行：带对令牌的握手仍是第一次握手，被拒的 Stop 也没有让插件停下
	h.handshake(ctx)
	if stops := read(h.seen, func() []string { return h.seen.stops }); len(stops) != 0 {
		t.Errorf("a refused Stop must not reach the hook, got %v", stops)
	}
}

// TestServeRejectsBadConfiguration 核对监听之前的核对（handshake.md §1 末段）：不合即不监听、不写握手行，stderr 记一条 error。
func TestServeRejectsBadConfiguration(t *testing.T) {
	registry := operation.NewRegistry()
	_ = operation.Add(registry, operation.Contract{ID: "orders.create"}, operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { return primitives.Ok(struct{}{}) }), nil, "")
	env := defaultEnv()
	missing := defaultEnv()
	delete(missing, EnvSession)
	withPipeline := strings.Replace(testManifest, `"health": ["echo"] }`, `"health": ["echo"], "pipeline": [{ "middleware": "audit", "stage": "observe" }] }`, 1)
	cases := []struct {
		name    string
		opts    Options
		env     map[string]string
		message string
	}{
		{"undeclared operation", Options{Manifest: []byte(testManifest), Operations: registry}, env, "orders.create"},
		{"missing session token", Options{Manifest: []byte(testManifest)}, missing, EnvSession},
		{"not a plugin", Options{Manifest: []byte(`{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability"}`)}, env, "out-of-process"},
		// 进程外插件不能贡献中间件：读清单即拒绝
		{"pipeline", Options{Manifest: []byte(withPipeline)}, env, "contributes.pipeline"},
		// primitives 必须等于本 SDK 支持的原语层大版本
		{"primitives", Options{Manifest: []byte(strings.Replace(testManifest, `"primitives": "1"`, `"primitives": "2"`, 1))}, env, "primitives 2"},
		{"catalog without en-US", Options{Manifest: []byte(testManifest), Catalogs: []Catalog{{Name: "probe", Locales: map[string]map[string]string{"zh-CN": {}}}}}, env, "en-US"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr syncBuffer
			environment := c.env
			c.opts.Getenv = func(key string) string { return environment[key] }
			c.opts.Stdout, c.opts.Stderr, c.opts.Stdin = &stdout, &stderr, strings.NewReader("")
			err := Serve(context.Background(), c.opts)
			if err == nil || !strings.Contains(err.Error(), c.message) {
				t.Errorf("expected an error mentioning %q, got %v", c.message, err)
			}
			if stdout.Len() != 0 {
				t.Errorf("no handshake line may be written: %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), `"level":"error"`) {
				t.Errorf("the refusal must be logged as an error, got %q", stderr.String())
			}
		})
	}
}

func TestServeRejectsBadSubscriptions(t *testing.T) {
	handler := func(*invocation.Context, Envelope, uint32) primitives.Result[primitives.Unit] {
		return primitives.Success()
	}
	subscribe := func(subscriptions ...Subscription) []SubscriptionHandler {
		handlers := make([]SubscriptionHandler, 0, len(subscriptions))
		for _, subscription := range subscriptions {
			handlers = append(handlers, SubscriptionHandler{Subscription: subscription, Handler: handler})
		}
		return handlers
	}
	// 不带会话令牌：订阅校验在读环境变量之前，合法的订阅走到缺令牌这一步才失败，借此区分是哪一条拒绝的
	env := defaultEnv()
	delete(env, EnvSession)
	serve := func(subscriptions []SubscriptionHandler) (string, error) {
		var stdout bytes.Buffer
		err := Serve(context.Background(), Options{Manifest: []byte(testManifest), Subscriptions: subscriptions, Getenv: func(key string) string { return env[key] }, Stdout: &stdout, Stderr: io.Discard, Stdin: strings.NewReader("")})
		return stdout.String(), err
	}

	rejected := []struct {
		name          string
		subscriptions []SubscriptionHandler
		message       string
	}{
		{"unknown mode", subscribe(Subscription{Topic: "probe.events", Mode: "fanout"}), "the mode 'fanout'"},
		{"mode in another case", subscribe(Subscription{Topic: "probe.events", Mode: "Broadcast"}), "the mode 'Broadcast'"},
		{"invalid topic", subscribe(Subscription{Topic: "Probe.Events"}), "valid topic"},
		// 主题的文法是 RFC-0009 的 ^[a-z0-9][a-z0-9._-]{0,127}$（rules.IsTopic）：长度、首字符、行尾与非 ASCII 的边界
		{"empty topic", subscribe(Subscription{Topic: ""}), "valid topic"},
		{"topic of 129 characters", subscribe(Subscription{Topic: strings.Repeat("a", 129)}), "valid topic"},
		{"topic starting with a dot", subscribe(Subscription{Topic: ".probe"}), "valid topic"},
		{"topic ending with a line feed", subscribe(Subscription{Topic: "probe.events\n"}), "valid topic"},
		{"topic with a non-ASCII letter", subscribe(Subscription{Topic: "probe.évents"}), "valid topic"},
		{"same pair twice", subscribe(Subscription{Topic: "probe.events"}, Subscription{Topic: "probe.events"}), "declared more than once"},
		{"empty and competing are the same mode", subscribe(Subscription{Topic: "probe.events"}, Subscription{Topic: "probe.events", Mode: SubscriptionModeCompeting}), "declared more than once"},
		{"one pair in both modes", subscribe(Subscription{Topic: "probe.events", MessageType: "probe.created", Mode: SubscriptionModeCompeting}, Subscription{Topic: "probe.events", MessageType: "probe.created", Mode: SubscriptionModeBroadcast}), "declared more than once"},
	}
	for _, test := range rejected {
		t.Run(test.name, func(t *testing.T) {
			stdout, err := serve(test.subscriptions)
			var framework *primitives.FrameworkError
			if !errors.As(err, &framework) || framework.Code != primitives.ErrorCodeInvalidArgument || !strings.Contains(err.Error(), test.message) {
				t.Errorf("expected an invalid-argument FrameworkError containing %q, got %v", test.message, err)
			}
			if stdout != "" {
				t.Errorf("no handshake line may be written for rejected subscriptions: %q", stdout)
			}
		})
	}

	// 同一主题的不同类型是不同的对，可以各用一种方式
	accepted := subscribe(
		Subscription{Topic: "probe.events", MessageType: "probe.created"},
		Subscription{Topic: "probe.events", MessageType: "probe.cleared", Mode: SubscriptionModeBroadcast},
		Subscription{Topic: "probe.events", Mode: SubscriptionModeCompeting},
	)
	if _, err := serve(accepted); err == nil || !strings.Contains(err.Error(), EnvSession) {
		t.Errorf("distinct (topic, type) pairs must pass the subscription checks, got %v", err)
	}
	// 文法之内的边界：128 个字符、数字开头、点、下划线与连字符
	for _, topic := range []string{strings.Repeat("a", 128), "0", "9probe._-events"} {
		if _, err := serve(subscribe(Subscription{Topic: topic})); err == nil || !strings.Contains(err.Error(), EnvSession) {
			t.Errorf("the topic %q must pass the subscription checks, got %v", topic, err)
		}
	}
}

// TestServeSendsTheManifestWithoutABOM 核对清单带 BOM 时照常读取，握手发的原文去掉 BOM（handshake.md §3 第 3 条）。
func TestServeSendsTheManifestWithoutABOM(t *testing.T) {
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
		opts.Manifest = append([]byte{0xEF, 0xBB, 0xBF}, opts.Manifest...)
	})
	if response := h.handshake(deadline(t)); response.Manifest != testManifest {
		t.Errorf("the manifest in the handshake must be the file without its BOM, got %q", response.Manifest)
	}
}

func TestNegotiationFollowsTheVectors(t *testing.T) {
	vectors, err := conformance.ProtocolVersions()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Cases {
		if len(c.Plugin) != 1 || c.Plugin[0] != ProtocolVersion {
			continue
		}
		selected, ok := negotiate(c.Host)
		if c.Error != "" {
			if ok {
				t.Errorf("host %v must fail", c.Host)
			}
		} else if !ok || selected != c.Selected {
			t.Errorf("host %v selected %d, want %d", c.Host, selected, c.Selected)
		}
	}
}

func TestLogHandlerWritesHostLines(t *testing.T) {
	var buffer bytes.Buffer
	logger := newTestLogger(&buffer)
	logger.Info("started", "operations", 2)
	logger.Warn("slow")
	logger.Error("failed")
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 3 || lines[0] != `{"level":"information","message":"started","operations":2}` || !strings.Contains(lines[1], `"level":"warning"`) || !strings.Contains(lines[2], `"level":"error"`) {
		t.Errorf("lines %v", lines)
	}
}

func TestLoadCatalogReadsLocaleFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"Localization/en-US.json": {Data: []byte(`{"echo.greeting":"Hello {name}"}`)},
		"Localization/zh-CN.json": {Data: []byte(`{"echo.greeting":"你好 {name}"}`)},
	}
	catalog, err := LoadCatalog("echo", fsys, "Localization")
	if err != nil || catalog.Name != "echo" || catalog.Locales["zh-CN"]["echo.greeting"] != "你好 {name}" {
		t.Fatalf("catalog %+v %v", catalog, err)
	}
	resolver := catalog.Resolver()
	if text, ok := resolver.Resolve("echo.greeting", "zh-CN", map[string]any{"name": "x"}); !ok || text != "你好 x" {
		t.Errorf("resolve %q", text)
	}
	// 解析器合并了 SDK 运行时的目录，运行时的错误也能按区域在本地渲染
	if text, ok := resolver.Resolve(MessageUnavailable, "zh-CN", map[string]any{"id": "p", "state": "Draining"}); !ok || text != "插件 'p' 不可用（状态：Draining）。" {
		t.Errorf("resolve the runtime key: %q %v", text, ok)
	}
	if _, err := LoadCatalog("echo", fstest.MapFS{"Localization/zh-CN.json": {Data: []byte(`{}`)}}, "Localization"); err == nil {
		t.Error("en-US is required")
	}
	if _, err := LoadCatalog("echo", fstest.MapFS{"Localization/en-US.json": {Data: []byte(`{"Bad Key":"x"}`)}}, "Localization"); err == nil {
		t.Error("keys are validated")
	}
}

// TestProtoErrorDetailsFollowTheValueModel 核对插件协议的错误细节按值模型读写（ADR-0236）：整数值的浮点带小数点，
// 超出 int64 的整数按浮点写，非有限值写成字符串；读回时 64 位范围内的整数是 int64，数组与对象是 []any 与 map[string]any
func TestProtoErrorDetailsFollowTheValueModel(t *testing.T) {
	err := primitives.Literal("ORDERS_UNEXPECTED_001", "x", map[string]any{"f": 2.0, "n": int64(9007199254740993), "nan": math.NaN(), "u": uint64(math.MaxUint64), "list": []any{1, "a"}}, nil)
	encoded := ToProtoError(err)
	if want := `{"f":2.0,"list":[1,"a"],"n":9007199254740993,"nan":"NaN","u":1.8446744073709552E+19}`; encoded.Details != want {
		t.Errorf("details %s, want %s", encoded.Details, want)
	}
	decoded := FromProtoError(encoded, probeID)
	if decoded.Details["f"] != 2.0 || decoded.Details["n"] != int64(9007199254740993) || decoded.Details["nan"] != "NaN" {
		t.Errorf("decoded %#v", decoded.Details)
	}
	if list, ok := decoded.Details["list"].([]any); !ok || list[0] != int64(1) || list[1] != "a" {
		t.Errorf("list %#v", decoded.Details["list"])
	}
}
