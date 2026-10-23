package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

const testManifest = `{
  "id": "xihan.samples.probe",
  "version": "0.1.0",
  "primitives": "1",
  "stability": "experimental",
  "kind": "plugin",
  "isolation": "out-of-process",
  "contributes": { "operations": ["echoes.create"], "config": { "section": "XiHan:Echo" }, "health": ["echo"] },
  "permissions": ["capability:kernel.pipeline"],
  "entry": "probe"
}`

type echoRequest struct {
	Text string `json:"text"`
}

type echoResponse struct {
	Text        string `json:"text"`
	Correlation string `json:"correlation"`
}

// harness 在进程内运行 Serve 并以 gRPC 客户端连上它。
type harness struct {
	client pluginv1.PluginClient
	stdin  *io.PipeWriter
	done   chan error
	stderr *bytes.Buffer
	seen   *recorder
}

// recorder 记录回调。
type recorder struct {
	configured Configuration
	started    bool
	stopReason string
	events     []string
}

func start(t *testing.T, env map[string]string) *harness {
	t.Helper()
	registry := operation.NewRegistry()
	handler := operation.HandlerFunc[echoRequest, echoResponse](func(ctx *invocation.Context, request echoRequest) primitives.Result[echoResponse] {
		if request.Text == "" {
			return primitives.Fail[echoResponse](primitives.Validation("PROBE_VALIDATION_001", "probe.text.missing", nil))
		}
		return primitives.Ok(echoResponse{Text: request.Text, Correlation: ctx.Correlation()})
	})
	if err := operation.Add(registry, operation.Contract{ID: "echoes.create", Stability: primitives.Experimental, Expose: []string{operation.ExposeHTTP}}, handler, nil, ""); err != nil {
		t.Fatal(err)
	}
	seen := &recorder{}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderr := &bytes.Buffer{}
	opts := Options{
		Manifest:   []byte(testManifest),
		Operations: registry,
		Subscriptions: []SubscriptionHandler{{
			Subscription: Subscription{Topic: "probe.events"},
			Handler: func(_ *invocation.Context, envelope Envelope, _ uint32) primitives.Result[primitives.Unit] {
				seen.events = append(seen.events, envelope.ID)
				return primitives.Success()
			},
		}},
		Catalogs: []Catalog{{Name: "probe", Locales: map[string]map[string]string{"en-US": {"probe.text.missing": "The text is required."}}}},
		Hooks: Hooks{
			Configure: func(_ context.Context, configuration Configuration) error {
				seen.configured = configuration
				return nil
			},
			Start: func(context.Context) error { seen.started = true; return nil },
			Stop:  func(_ context.Context, reason string, _ time.Duration) error { seen.stopReason = reason; return nil },
			Health: func() Health {
				return Health{State: primitives.Ready, Items: []HealthItem{{Name: "echo", Status: Healthy}, {Name: "undeclared", Status: Healthy}}}
			},
		},
		Stdin:     stdinReader,
		Stdout:    stdoutWriter,
		Stderr:    stderr,
		Getenv:    func(key string) string { return env[key] },
		StopGrace: 2 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), opts) }()

	line, err := bufio.NewReader(stdoutReader).ReadString('\n')
	if err != nil {
		t.Fatalf("handshake line: %v (serve: %v)", err, <-done)
	}
	go func() { _, _ = io.Copy(io.Discard, stdoutReader) }()
	if !strings.HasPrefix(line, HandshakeLineVersion+" ") {
		t.Fatalf("handshake line %q", line)
	}
	endpoint, ok := ParseEndpoint(strings.TrimPrefix(strings.TrimSpace(line), HandshakeLineVersion+" "))
	if !ok {
		t.Fatalf("handshake line %q", line)
	}
	conn, err := dial(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &harness{client: pluginv1.NewPluginClient(conn), stdin: stdinWriter, done: done, stderr: stderr, seen: seen}
}

func defaultEnv() map[string]string {
	return map[string]string{EnvProtocolVersions: "1", EnvPrimitives: "1", EnvSession: "session-token", EnvInstance: "probe#1", EnvLocale: "en-US"}
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestServeRunsTheProtocol(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)

	early, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{"text":"x"}`)})
	if err != nil || early.Status.Ok || early.Status.Error.Code != ErrorCodeUnavailable {
		t.Fatalf("an invoke before Start must be unavailable: %v %v", early, err)
	}

	handshake, err := h.client.Handshake(ctx, &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{1, 2}, Primitives: 1, InstanceId: "probe", DefaultLocale: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	if handshake.ProtocolVersion != 1 || handshake.Primitives != 1 || handshake.Manifest != testManifest || handshake.Sdk.Name != primitives.SDKName || handshake.Sdk.Language != "go" {
		t.Errorf("handshake %+v", handshake)
	}
	if len(handshake.Operations) != 1 || handshake.Operations[0].Id != "echoes.create" || handshake.Operations[0].Stability != "experimental" || handshake.Operations[0].Expose[0] != "http" {
		t.Errorf("operations %+v", handshake.Operations)
	}
	if len(handshake.Subscriptions) != 1 || handshake.Subscriptions[0].Topic != "probe.events" || len(handshake.Catalogs) != 1 || handshake.Catalogs[0].Locales[0].Locale != "en-US" {
		t.Errorf("subscriptions %+v catalogs %+v", handshake.Subscriptions, handshake.Catalogs)
	}
	if _, err := h.client.Handshake(ctx, &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{1}}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("a second handshake must be refused, got %v", err)
	}

	configured, err := h.client.Configure(ctx, &pluginv1.ConfigureRequest{Configuration: `{"greeting":"hi"}`, GrantedPermissions: []string{"capability:kernel.pipeline"}, WorkDirectory: t.TempDir(), Bindings: []*pluginv1.Binding{{Name: "cache", Type: "cache:distributed", Value: "redis://localhost"}}})
	if err != nil || !configured.Status.Ok {
		t.Fatalf("configure %v %v", configured, err)
	}
	if string(h.seen.configured.Section) != `{"greeting":"hi"}` || len(h.seen.configured.Bindings) != 1 || h.seen.configured.Bindings[0].Value != "redis://localhost" {
		t.Errorf("configuration %+v", h.seen.configured)
	}
	if _, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create"}); err != nil {
		t.Fatal(err)
	}
	started, err := h.client.Start(ctx, &pluginv1.StartRequest{})
	if err != nil || !started.Status.Ok || !h.seen.started {
		t.Fatalf("start %v %v", started, err)
	}

	health, err := h.client.Health(ctx, &pluginv1.HealthRequest{})
	if err != nil || health.State != pluginv1.LifecycleState_LIFECYCLE_STATE_READY || len(health.Items) != 1 || health.Items[0].Name != "echo" {
		t.Errorf("health %v %v", health, err)
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
	failed, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{}`)})
	if err != nil || failed.Status.Ok || failed.Status.Error.Code != "PROBE_VALIDATION_001" || failed.Status.Error.MessageKey != "probe.text.missing" {
		t.Errorf("business failure %v %v", failed, err)
	}
	if _, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.missing"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("unknown operation must be Unimplemented, got %v", err)
	}
	if _, err := h.client.InvokeStream(ctx); err != nil {
		t.Fatal(err)
	}

	event, err := h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: &pluginv1.Envelope{Topic: "probe.events", Id: "e1", Type: "probe.created", SpecVersion: "1.0"}, Attempt: 1})
	if err != nil || !event.Status.Ok || len(h.seen.events) != 1 || h.seen.events[0] != "e1" {
		t.Errorf("event %v %v %v", event, err, h.seen.events)
	}
	unknown, err := h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: &pluginv1.Envelope{Topic: "other.events", Id: "e2"}, Attempt: 1})
	if err != nil || unknown.Status.Ok || unknown.Status.Error.Code != operation.ErrorCodeNotFound {
		t.Errorf("undeclared topic %v %v", unknown, err)
	}

	// 停止时仍打开的 WatchHealth 流必须随 Draining 结束，不能拖住优雅停止
	openWatch, err := h.client.WatchHealth(ctx, &pluginv1.HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openWatch.Recv(); err != nil {
		t.Fatal(err)
	}
	stopping := time.Now()
	stopped, err := h.client.Stop(ctx, &pluginv1.StopRequest{GraceSeconds: 5, Reason: "unload"})
	if err != nil || !stopped.Status.Ok || h.seen.stopReason != "unload" {
		t.Fatalf("stop %v %v", stopped, err)
	}
	for {
		update, err := openWatch.Recv()
		if err != nil {
			break
		}
		if update.State == pluginv1.LifecycleState_LIFECYCLE_STATE_DRAINING {
			continue
		}
		t.Errorf("unexpected health update after Stop: %v", update)
	}
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("serve returned %v", err)
		}
		if time.Since(stopping) > 2*time.Second {
			t.Errorf("Serve took %s to return after Stop; the open stream held the graceful stop", time.Since(stopping))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve must return after Stop")
	}
	if !strings.Contains(h.stderr.String(), `"level":"information"`) || !strings.Contains(h.stderr.String(), `"message":"handshake completed"`) {
		t.Errorf("stderr %s", h.stderr.String())
	}
}

func TestServeStopsWhenStdinCloses(t *testing.T) {
	h := start(t, defaultEnv())
	if _, err := h.client.Handshake(deadline(t), &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{1}, Primitives: 1}); err != nil {
		t.Fatal(err)
	}
	_ = h.stdin.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve must return after stdin closes")
	}
}

func TestServeRefusesWithoutACommonVersion(t *testing.T) {
	h := start(t, defaultEnv())
	_, err := h.client.Handshake(deadline(t), &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{99}, Primitives: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %v", err)
	}
	_ = h.stdin.Close()
	<-h.done
}

func TestServeRejectsBadConfiguration(t *testing.T) {
	registry := operation.NewRegistry()
	_ = operation.Add(registry, operation.Contract{ID: "orders.create"}, operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { return primitives.Ok(struct{}{}) }), nil, "")
	env := defaultEnv()
	if err := Serve(context.Background(), Options{Manifest: []byte(testManifest), Operations: registry, Getenv: func(key string) string { return env[key] }, Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader("")}); err == nil {
		t.Error("an undeclared operation must be rejected")
	}
	missing := defaultEnv()
	delete(missing, EnvSession)
	if err := Serve(context.Background(), Options{Manifest: []byte(testManifest), Getenv: func(key string) string { return missing[key] }, Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader("")}); err == nil {
		t.Error("a missing session token must be rejected")
	}
	if err := Serve(context.Background(), Options{Manifest: []byte(`{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability"}`), Getenv: func(key string) string { return env[key] }, Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader("")}); err == nil {
		t.Error("a non-plugin manifest must be rejected")
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

func TestStatusAndMetadataConversions(t *testing.T) {
	inner := primitives.Literal("KERNEL_UNEXPECTED_001", "boom", nil, nil)
	err := primitives.Unexpected("ORDERS_UNEXPECTED_001", "orders.failed", inner, map[string]any{"id": "o1"})
	encoded := ToProtoError(err)
	if encoded.Code != "ORDERS_UNEXPECTED_001" || encoded.MessageKey != "orders.failed" || encoded.Details != `{"id":"o1"}` || encoded.Inner.Text != "boom" {
		t.Errorf("encoded %+v", encoded)
	}
	decoded := FromProtoError(encoded)
	if decoded.Code != err.Code || decoded.Details["id"] != "o1" || decoded.Inner == nil || decoded.Inner.Code != "KERNEL_UNEXPECTED_001" {
		t.Errorf("decoded %+v", decoded)
	}
	if bad := FromProtoError(&pluginv1.Error{Code: "bad"}); bad.Code != ErrorCodeUnexpected || bad.Details["code"] != "bad" {
		t.Errorf("bad code %+v", bad)
	}
	if !FromStatus(&pluginv1.Status{Ok: true}).OK() || FromStatus(nil).OK() || FromStatus(&pluginv1.Status{}).Err().Code != ErrorCodeUnexpected {
		t.Error("status conversion")
	}
	if StatusFromError(io.EOF).Err().Code != ErrorCodeUnexpected || StatusFromError(err).Err() != err {
		t.Error("status from error")
	}

	deadlineText := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	ctx := FromMetadata(context.Background(), &pluginv1.InvocationMetadata{TraceParent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", Deadline: deadlineText, Baggage: map[string]string{"xihan.tenant": "t1", "bad key": "x", "xihan.custom": "c"}})
	if ctx.TraceID() != "0af7651916cd43dd8448eb211c80319c" || !ctx.HasDeadline() || ctx.Tenant() != "t1" {
		t.Errorf("context %v", ctx)
	}
	if _, ok := ctx.GetBaggage("bad key"); ok {
		t.Error("malformed keys are dropped")
	}
	metadata := ToMetadata(ctx)
	if metadata.TraceParent != ctx.TraceParent() || metadata.Deadline == "" || metadata.Baggage["xihan.tenant"] != "t1" {
		t.Errorf("metadata %+v", metadata)
	}
	if _, ok := metadata.Baggage["xihan.custom"]; ok {
		t.Error("unregistered keys are stripped on egress")
	}
	grpcCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if !FromMetadata(grpcCtx, nil).HasDeadline() {
		t.Error("the gRPC deadline is used without metadata")
	}
}

func TestGrpcErrorsMapToErrorCodes(t *testing.T) {
	cases := map[codes.Code]string{
		codes.Unavailable:      ErrorCodeUnavailable,
		codes.DeadlineExceeded: invocation.ErrorCodeDeadlineExceeded,
		codes.Canceled:         invocation.ErrorCodeCancelled,
		codes.PermissionDenied: ErrorCodeCallbackDenied,
		codes.Unauthenticated:  ErrorCodeCallbackDenied,
		codes.Unimplemented:    operation.ErrorCodeNotFound,
		codes.InvalidArgument:  operation.ErrorCodeInvalidPayload,
		codes.Internal:         ErrorCodeUnexpected,
	}
	for code, want := range cases {
		if got := FromGrpcError(status.Error(code, "x")).Code; got != want {
			t.Errorf("%s -> %s, want %s", code, got, want)
		}
	}
	if FromGrpcError(nil) != nil {
		t.Error("nil")
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
	if text, ok := catalog.Resolver().Resolve("echo.greeting", "zh-CN", map[string]any{"name": "x"}); !ok || text != "你好 x" {
		t.Errorf("resolve %q", text)
	}
	if _, err := LoadCatalog("echo", fstest.MapFS{"Localization/zh-CN.json": {Data: []byte(`{}`)}}, "Localization"); err == nil {
		t.Error("en-US is required")
	}
	if _, err := LoadCatalog("echo", fstest.MapFS{"Localization/en-US.json": {Data: []byte(`{"Bad Key":"x"}`)}}, "Localization"); err == nil {
		t.Error("keys are validated")
	}
}

func TestParseEndpoint(t *testing.T) {
	if endpoint, ok := ParseEndpoint(`npipe \\.\pipe\xihan-host`); !ok || endpoint.Network != NetworkPipe || endpoint.Address != `\\.\pipe\xihan-host` {
		t.Errorf("endpoint %+v", endpoint)
	}
	for _, bad := range []string{"", "unix", "udp 127.0.0.1:1"} {
		if _, ok := ParseEndpoint(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
}
