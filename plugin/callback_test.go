package plugin

import (
	"context"
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-spec/conformance"
	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/invocation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// fakeHost 扮演宿主的 Host 服务：记下每次回调的请求、会话令牌与 gRPC 截止时间，按测试给的函数应答。
type fakeHost struct {
	pluginv1.UnimplementedHostServer
	mu        sync.Mutex
	invokes   []*pluginv1.InvokeRequest
	publishes []*pluginv1.PublishRequest
	sessions  [][]string
	deadlines []time.Time
	invoke    func(*pluginv1.InvokeRequest) (*pluginv1.InvokeResponse, error)
	publish   func(*pluginv1.PublishRequest) (*pluginv1.PublishResponse, error)
}

// startFakeHost 在回环 TCP 上启动假的 Host 服务，返回它与 XIHAN_PLUGIN_HOST_ADDRESS 的值。
func startFakeHost(t *testing.T) (*fakeHost, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{}
	server := grpc.NewServer()
	pluginv1.RegisterHostServer(server, host)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return host, "tcp 127.0.0.1:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

// observe 记下一次回调的会话令牌与截止时间。
func (f *fakeHost) observe(ctx context.Context) {
	incoming, _ := metadata.FromIncomingContext(ctx)
	deadline, _ := ctx.Deadline()
	f.sessions = append(f.sessions, incoming.Get(SessionMetadataKey))
	f.deadlines = append(f.deadlines, deadline)
}

// Invoke 实现 Host.Invoke。
func (f *fakeHost) Invoke(ctx context.Context, request *pluginv1.InvokeRequest) (*pluginv1.InvokeResponse, error) {
	f.mu.Lock()
	f.observe(ctx)
	f.invokes = append(f.invokes, request)
	answer := f.invoke
	f.mu.Unlock()
	if answer != nil {
		return answer(request)
	}
	return &pluginv1.InvokeResponse{Status: &pluginv1.Status{Ok: true}, Payload: []byte(`{"echo":true}`)}, nil
}

// Publish 实现 Host.Publish。
func (f *fakeHost) Publish(ctx context.Context, request *pluginv1.PublishRequest) (*pluginv1.PublishResponse, error) {
	f.mu.Lock()
	f.observe(ctx)
	f.publishes = append(f.publishes, request)
	answer := f.publish
	f.mu.Unlock()
	if answer != nil {
		return answer(request)
	}
	response := &pluginv1.PublishResponse{}
	for range request.Envelopes {
		response.Results = append(response.Results, &pluginv1.Status{Ok: true})
	}
	return response, nil
}

// answer 换掉应答函数。
func (f *fakeHost) answer(invoke func(*pluginv1.InvokeRequest) (*pluginv1.InvokeResponse, error), publish func(*pluginv1.PublishRequest) (*pluginv1.PublishResponse, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invoke, f.publish = invoke, publish
}

// calls 返回收到的回调次数。
func (f *fakeHost) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.invokes) + len(f.publishes)
}

// runCallbacks 启动测试插件：每次 probes.run 取出 pending 里的下一个回调在调用上下文里执行，结果送回 results。
func runCallbacks(t *testing.T, env map[string]string, mutate func(opts *Options)) (*harness, chan func(ctx *invocation.Context, host *Host) any, chan any) {
	t.Helper()
	pending, results := make(chan func(ctx *invocation.Context, host *Host) any, 16), make(chan any, 16)
	h := launch(t, env, func(opts *Options, seen *recorder) {
		seen.run = func(ctx *invocation.Context) *primitives.Error {
			host, ok := HostOf(ctx)
			if !ok {
				t.Error("every call carries the host client")
				return nil
			}
			results <- (<-pending)(ctx, host)
			return nil
		}
		if mutate != nil {
			mutate(opts)
		}
	})
	return h, pending, results
}

// call 让插件在一次 probes.run 里执行 do，返回它的结果；metadata 是这次调用的元数据。
func call(t *testing.T, h *harness, pending chan func(ctx *invocation.Context, host *Host) any, results chan any, metadata *pluginv1.InvocationMetadata, do func(ctx *invocation.Context, host *Host) any) any {
	t.Helper()
	pending <- do
	invoked, err := h.client.Invoke(deadline(t), &pluginv1.InvokeRequest{Operation: "probes.run", Metadata: metadata})
	if err != nil || !invoked.Status.Ok {
		t.Fatalf("probes.run %v %v", invoked, err)
	}
	return <-results
}

// hostInvoke 回调 Host.Invoke 调 orders.create。
func hostInvoke(ctx *invocation.Context, host *Host) any {
	return host.Invoke(ctx, "orders.create", []byte(`{}`)).Outcome.Err()
}

// hostPublish 回调 Host.Publish 发两条消息。
func hostPublish(ctx *invocation.Context, host *Host) any {
	return host.Publish(ctx, Envelope{Topic: "orders.events", ID: "m1"}, Envelope{Topic: "orders.events", ID: "m2"})
}

// expectEvery 核对整批结果的每一项都是同一个错误。
func expectEvery(t *testing.T, what string, results any, count int, code, key string, details map[string]any) {
	t.Helper()
	items := results.([]primitives.Result[primitives.Unit])
	if len(items) != count {
		t.Fatalf("%s: %d results, want %d", what, len(items), count)
	}
	for index, item := range items {
		expectError(t, what+" item "+strconv.Itoa(index), item.Err(), code, key, details)
	}
}

// TestCallbacksWithoutAnEndpointAreUnavailable 核对 handshake.md §4：XIHAN_PLUGIN_HOST_ADDRESS 缺失、不合法或没有能连的项时照常服务，
// 启动时记一条 warning，每次回调都是 PLUGINS_UNAVAILABLE_001 + plugins.host.unavailable、不发请求。
func TestCallbacksWithoutAnEndpointAreUnavailable(t *testing.T) {
	for _, value := range []string{"", "unix /tmp/a b", "udp 127.0.0.1:1", "tcp 10.0.0.1:50123", "unix /tmp/a;"} {
		t.Run(value, func(t *testing.T) {
			env := defaultEnv()
			if value != "" {
				env[EnvHostAddress] = value
			}
			h, pending, results := runCallbacks(t, env, nil)
			if !strings.Contains(h.stderr.String(), `"level":"warning"`) || !strings.Contains(h.stderr.String(), "no supported callback endpoint") {
				t.Errorf("a warning must be logged at startup, got %s", h.stderr.String())
			}
			ctx := deadline(t)
			h.ready(ctx, nil)
			unavailable := map[string]any{"id": probeID, "reason": "no supported callback endpoint"}
			expectError(t, "Host.Invoke", call(t, h, pending, results, nil, hostInvoke).(*primitives.Error), ErrorCodeUnavailable, MessageHostUnavailable, unavailable)
			expectEvery(t, "Host.Publish", call(t, h, pending, results, nil, hostPublish), 2, ErrorCodeUnavailable, MessageHostUnavailable, unavailable)
		})
	}
}

// TestCallbacksAreCheckedLocally 核对 invocation.md §5：权限既在清单 permissions 里、也在 granted_permissions 里，租户是某个在途调用的租户，
// 否则不发请求、与宿主给出同样的错误；Host.Publish 两项都是整批的结果。
func TestCallbacksAreCheckedLocally(t *testing.T) {
	host, address := startFakeHost(t)
	env := defaultEnv()
	env[EnvHostAddress] = address

	t.Run("not granted", func(t *testing.T) {
		h, pending, results := runCallbacks(t, env, nil)
		h.ready(deadline(t), []string{})
		before := host.calls()
		expectError(t, "Host.Invoke", call(t, h, pending, results, nil, hostInvoke).(*primitives.Error), ErrorCodeCallbackDenied, MessageCallbackDenied, map[string]any{"id": probeID, "permission": "capability:kernel.pipeline"})
		expectEvery(t, "Host.Publish", call(t, h, pending, results, nil, hostPublish), 2, ErrorCodeCallbackDenied, MessageCallbackDenied, map[string]any{"id": probeID, "permission": "capability:messaging.queue"})
		if host.calls() != before {
			t.Error("a callback refused locally must not reach the host")
		}
	})
	t.Run("not declared", func(t *testing.T) {
		// 宿主授予了而清单没有声明的权限不放行回调（清单是插件权限的唯一事实源）
		h, pending, results := runCallbacks(t, env, func(opts *Options) {
			opts.Manifest = []byte(strings.Replace(testManifest, `, "capability:messaging.queue"]`, `]`, 1))
		})
		h.ready(deadline(t), nil)
		expectEvery(t, "Host.Publish", call(t, h, pending, results, nil, hostPublish), 2, ErrorCodeCallbackDenied, MessageCallbackDenied, map[string]any{"id": probeID, "permission": "capability:messaging.queue"})
		if err := call(t, h, pending, results, nil, hostInvoke); err.(*primitives.Error) != nil {
			t.Errorf("the declared and granted permission passes, got %v", err)
		}
	})
	t.Run("tenant", func(t *testing.T) {
		h, pending, results := runCallbacks(t, env, nil)
		h.ready(deadline(t), nil)
		withTenant := &pluginv1.InvocationMetadata{Baggage: map[string]string{primitives.BaggageTenant: "t1"}}
		host.mu.Lock()
		before := len(host.invokes)
		host.mu.Unlock()
		if err := call(t, h, pending, results, withTenant, hostInvoke); err.(*primitives.Error) != nil {
			t.Fatalf("the tenant of the call in flight may call back, got %v", err)
		}
		host.mu.Lock()
		reached := host.invokes[before]
		host.mu.Unlock()
		if reached.Metadata.GetBaggage()[primitives.BaggageTenant] != "t1" || reached.Operation != "orders.create" {
			t.Errorf("the callback must reach the host with the tenant, got %+v", reached)
		}
		other := func(do func(ctx *invocation.Context, host *Host) any) func(ctx *invocation.Context, host *Host) any {
			return func(ctx *invocation.Context, host *Host) any {
				return do(ctx.WithBaggage(primitives.BaggageTenant, "t2"), host)
			}
		}
		denied := map[string]any{"id": probeID, "tenant": "t2"}
		expectError(t, "Host.Invoke in another tenant", call(t, h, pending, results, withTenant, other(hostInvoke)).(*primitives.Error), ErrorCodeCallbackDenied, MessageTenantDenied, denied)
		expectEvery(t, "Host.Publish in another tenant", call(t, h, pending, results, withTenant, other(hostPublish)), 2, ErrorCodeCallbackDenied, MessageTenantDenied, denied)
		// 信封的 xihantenant 不在本地核对，交给宿主逐项判定
		envelopeTenant := func(ctx *invocation.Context, host *Host) any {
			return host.Publish(ctx, Envelope{Topic: "orders.events", ID: "m1", Extensions: map[string]string{"xihantenant": "t9"}})
		}
		if outcome := call(t, h, pending, results, nil, envelopeTenant).([]primitives.Result[primitives.Unit]); len(outcome) != 1 || !outcome[0].OK() {
			t.Errorf("the envelope's tenant is left to the host, got %v", outcome)
		}
		// 调用结束之后在后台回调：那个租户已经没有在途调用
		var kept *invocation.Context
		var keptHost *Host
		call(t, h, pending, results, withTenant, func(ctx *invocation.Context, host *Host) any { kept, keptHost = ctx, host; return nil })
		background := invocation.Background(invocation.WithBaggage(kept.Baggage()))
		expectError(t, "a callback after the call ended", hostInvoke(background, keptHost).(*primitives.Error), ErrorCodeCallbackDenied, MessageTenantDenied, map[string]any{"id": probeID, "tenant": "t1"})
	})
}

// sevenDecimals 是回调写出的 deadline 写法：UTC、七位小数、+00:00（invocation.md §2）。
var sevenDecimals = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{7}\+00:00$`)

// TestCallbackMetadataComesFromTheCall 核对 invocation.md §5：回调的 metadata 取自所在的调用上下文（traceparent、按 §2 写的 deadline、
// 已登记的 baggage、空的 trace_state），gRPC 截止时间与 deadline 相同，带且只带一个会话令牌。
func TestCallbackMetadataComesFromTheCall(t *testing.T) {
	host, address := startFakeHost(t)
	env := defaultEnv()
	env[EnvHostAddress] = address
	h, pending, results := runCallbacks(t, env, nil)
	h.ready(deadline(t), nil)
	inbound := &pluginv1.InvocationMetadata{
		TraceParent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		TraceState:  "vendor=1",
		// 早于测试调用的 gRPC 截止时间（10 秒），生效的就是它
		Deadline: rules.FormatDeadline(time.Now().Add(5 * time.Second)),
		Baggage:  map[string]string{primitives.BaggageCorrelation: "c1", "xihan.custom": "x"},
	}
	var traceParent string
	call(t, h, pending, results, inbound, func(ctx *invocation.Context, host *Host) any {
		traceParent = ctx.TraceParent()
		return hostInvoke(ctx, host)
	})
	host.mu.Lock()
	defer host.mu.Unlock()
	sent := host.invokes[len(host.invokes)-1].Metadata
	if sent.TraceParent != traceParent || sent.TraceState != "" || len(sent.Baggage) != 1 || sent.Baggage[primitives.BaggageCorrelation] != "c1" {
		t.Errorf("metadata %+v", sent)
	}
	if !sevenDecimals.MatchString(sent.Deadline) || sent.Deadline != inbound.Deadline {
		t.Errorf("the deadline must be written as UTC with seven decimals, got %q want %q", sent.Deadline, inbound.Deadline)
	}
	written, _ := rules.ParseDeadline(sent.Deadline)
	if received := host.deadlines[len(host.deadlines)-1]; received.IsZero() || received.Sub(written).Abs() > time.Second {
		t.Errorf("the gRPC deadline %v must equal the written deadline %v", received, written)
	}
	if sessions := host.sessions[len(host.sessions)-1]; len(sessions) != 1 || sessions[0] != env[EnvSession] {
		t.Errorf("the callback carries exactly the session token, got %v", sessions)
	}
}

// TestCallbackStatusFollowsTheVectors 回放 callback-status.json（invocation.md §5）：映射本身逐项回放；调用上下文没有取消的用例
// 另经假的 Host 服务端到端回放，Host.Publish 的 RPC 失败每一项都是同一个错误。
func TestCallbackStatusFollowsTheVectors(t *testing.T) {
	vectors, err := conformance.CallbackStatus()
	if err != nil {
		t.Fatal(err)
	}
	detailsOf := func(raw json.RawMessage, pluginID string) map[string]any {
		var details map[string]any
		if err := json.Unmarshal(raw, &details); err != nil {
			t.Fatal(err)
		}
		if details != nil && details["id"] == vectors.PluginID {
			details["id"] = pluginID
		}
		return details
	}
	mapping := &Host{pluginID: vectors.PluginID}
	for _, c := range vectors.Cases {
		ctx := invocation.Background()
		if c.ContextCancelled {
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			ctx = invocation.New(cancelled)
		}
		got := mapping.failure(ctx, c.RPC, status.Error(codes.Code(c.StatusCode), c.Message), vectors.Operation)
		expectError(t, c.RPC+" "+c.Status, got, c.Code, c.Key, detailsOf(c.Details, vectors.PluginID))
	}

	host, address := startFakeHost(t)
	env := defaultEnv()
	env[EnvHostAddress] = address
	h, pending, results := runCallbacks(t, env, nil)
	h.ready(deadline(t), nil)
	for _, c := range vectors.Cases {
		if c.ContextCancelled {
			continue
		}
		failure := status.Error(codes.Code(c.StatusCode), c.Message)
		host.answer(func(*pluginv1.InvokeRequest) (*pluginv1.InvokeResponse, error) { return nil, failure }, func(*pluginv1.PublishRequest) (*pluginv1.PublishResponse, error) { return nil, failure })
		details := detailsOf(c.Details, probeID)
		if c.RPC == rules.CallbackInvoke {
			expectError(t, "end to end invoke "+c.Status, call(t, h, pending, results, nil, hostInvoke).(*primitives.Error), c.Code, c.Key, details)
		} else {
			expectEvery(t, "end to end publish "+c.Status, call(t, h, pending, results, nil, hostPublish), 2, c.Code, c.Key, details)
		}
	}
	host.answer(nil, nil)

	// 调用方取消的回调：gRPC 在本地以 Cancelled 结束，回调上下文的取消信号已触发
	cancelled := call(t, h, pending, results, nil, func(ctx *invocation.Context, host *Host) any {
		derived, cancel := context.WithCancel(ctx)
		cancel()
		return hostInvoke(invocation.New(derived), host)
	})
	expectError(t, "a cancelled callback", cancelled.(*primitives.Error), "KERNEL_CANCELLED_001", "kernel.invocation.cancelled", nil)
}

// TestCallbackConnectionFailureIsUnavailable 核对连接失败按 Unavailable 映射成 plugins.host.unavailable（invocation.md §5）。
func TestCallbackConnectionFailureIsUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "tcp " + listener.Addr().String()
	_ = listener.Close()
	env := defaultEnv()
	env[EnvHostAddress] = address
	h, pending, results := runCallbacks(t, env, nil)
	h.ready(deadline(t), nil)
	failure := call(t, h, pending, results, nil, hostInvoke).(*primitives.Error)
	if failure == nil || failure.Code != ErrorCodeUnavailable || failure.MessageKey != MessageHostUnavailable || !strings.HasPrefix(failure.Details["reason"].(string), "Unavailable: ") || failure.Details["id"] != probeID {
		t.Errorf("a connection failure %+v", failure)
	}
}

// TestPublishResultsFollowTheBatchRules 核对 invocation.md §5：结果数与信封数不同时每一项都是 plugins.process.failed（phase 为 publish）；
// 各项 Status 按 §3 读入；空的信封列表不发请求。
func TestPublishResultsFollowTheBatchRules(t *testing.T) {
	host, address := startFakeHost(t)
	env := defaultEnv()
	env[EnvHostAddress] = address
	h, pending, results := runCallbacks(t, env, nil)
	h.ready(deadline(t), nil)

	host.answer(nil, func(*pluginv1.PublishRequest) (*pluginv1.PublishResponse, error) {
		return &pluginv1.PublishResponse{Results: []*pluginv1.Status{{Ok: true}}}, nil
	})
	expectEvery(t, "a short result list", call(t, h, pending, results, nil, hostPublish), 2, ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "publish", "reason": "the host returned 1 results for 2 envelopes"})

	host.answer(func(*pluginv1.InvokeRequest) (*pluginv1.InvokeResponse, error) {
		return &pluginv1.InvokeResponse{Status: &pluginv1.Status{Ok: false}}, nil
	}, func(*pluginv1.PublishRequest) (*pluginv1.PublishResponse, error) {
		return &pluginv1.PublishResponse{Results: []*pluginv1.Status{{Ok: true}, {Ok: false, Error: &pluginv1.Error{Code: "MESSAGING_VALIDATION_002", MessageKey: "messaging.topic.invalid"}}, {Ok: false}}}, nil
	})
	three := call(t, h, pending, results, nil, func(ctx *invocation.Context, host *Host) any {
		return host.Publish(ctx, Envelope{Topic: "a", ID: "1"}, Envelope{ID: "2"}, Envelope{Topic: "c", ID: "3"})
	}).([]primitives.Result[primitives.Unit])
	if !three[0].OK() {
		t.Errorf("item 0 %v", three[0].Err())
	}
	expectError(t, "item 1", three[1].Err(), "MESSAGING_VALIDATION_002", "messaging.topic.invalid", nil)
	expectError(t, "item 2", three[2].Err(), ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "publish", "reason": "the host reported a failure without an error"})
	expectError(t, "an invoke failure without an error", call(t, h, pending, results, nil, hostInvoke).(*primitives.Error), ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "orders.create", "reason": "the host reported a failure without an error"})

	before := host.calls()
	if empty := call(t, h, pending, results, nil, func(ctx *invocation.Context, host *Host) any { return host.Publish(ctx) }).([]primitives.Result[primitives.Unit]); len(empty) != 0 || host.calls() != before {
		t.Errorf("an empty batch sends nothing, got %v", empty)
	}
}
