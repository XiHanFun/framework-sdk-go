package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-sdk-go/invocation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// stateOf 是协议的状态枚举。
func stateOf(state primitives.LifecycleState) pluginv1.LifecycleState {
	return pluginv1.LifecycleState(state)
}

// TestCallsOutOfOrderAreRefused 核对 lifecycle.md §7.1：不在次序表里的组合以 FailedPrecondition 拒绝、什么都不做、状态不变。
func TestCallsOutOfOrderAreRefused(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)
	refused := func(what string, err error) {
		t.Helper()
		if status.Code(err) != codes.FailedPrecondition {
			t.Errorf("%s must be refused with FailedPrecondition, got %v", what, err)
		}
	}
	expectState := func(when string, want primitives.LifecycleState) {
		t.Helper()
		if health := h.health(ctx); health.State != stateOf(want) || len(health.Items) != 0 {
			t.Errorf("Health %s must report %s without items, got %v", when, want, health)
		}
	}

	_, err := h.client.Configure(ctx, &pluginv1.ConfigureRequest{Configuration: "{}"})
	refused("Configure before Handshake", err)
	_, err = h.client.Start(ctx, &pluginv1.StartRequest{})
	refused("Start before Handshake", err)
	_, err = h.client.Stop(ctx, &pluginv1.StopRequest{Reason: "unload"})
	refused("Stop before Handshake", err)
	expectState("before Handshake", primitives.Declared)

	h.handshake(ctx)
	_, err = h.client.Handshake(ctx, &pluginv1.HandshakeRequest{ProtocolVersions: []uint32{1}, Primitives: 1})
	refused("a second Handshake", err)
	_, err = h.client.Start(ctx, &pluginv1.StartRequest{})
	refused("Start without Configure", err)
	expectState("after Handshake", primitives.Resolved)

	if configured := h.configure(ctx, "{}", nil); !configured.Ok {
		t.Fatalf("configure %v", configured)
	}
	_, err = h.client.Configure(ctx, &pluginv1.ConfigureRequest{Configuration: "{}"})
	refused("a second Configure", err)
	expectState("after Configure", primitives.Configured)
	// Start 之前不调健康回调
	if healths := read(h.seen, func() int { return h.seen.healths }); healths != 0 {
		t.Errorf("the health hook must not run before Start, ran %d times", healths)
	}

	started, err := h.client.Start(ctx, &pluginv1.StartRequest{})
	if err != nil || !started.Status.Ok {
		t.Fatalf("start %v %v", started, err)
	}
	_, err = h.client.Start(ctx, &pluginv1.StartRequest{})
	refused("a second Start", err)
	if health := h.health(ctx); health.State != stateOf(primitives.Ready) {
		t.Errorf("Health after Start %v", health)
	}
}

// TestConcurrentConfigureIsAcceptedOnce 核对配置回调执行期间（状态仍是 Resolved）的第二次 Configure 同样拒绝（lifecycle.md §7.1）。
func TestConcurrentConfigureIsAcceptedOnce(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
		opts.Hooks.Configure = func(context.Context, Configuration) error {
			close(entered)
			<-release
			return nil
		}
	})
	ctx := deadline(t)
	h.handshake(ctx)
	first := make(chan *pluginv1.Status, 1)
	go func() { first <- h.configure(ctx, "{}", nil) }()
	<-entered
	if _, err := h.client.Configure(ctx, &pluginv1.ConfigureRequest{Configuration: "{}"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("a Configure while the first one runs must be refused, got %v", err)
	}
	close(release)
	if configured := <-first; !configured.Ok {
		t.Errorf("the first Configure must succeed, got %v", configured)
	}
}

// TestConfigurationMustBeAnObject 核对 lifecycle.md §7.2：空等同 {}；不是 JSON 对象（含通不过共同 JSON 读取检查的）时不调配置回调，
// 以 plugins.process.failed 失败并进入 Failed；之后 Stop 应答 ok，不调停止回调。
func TestConfigurationMustBeAnObject(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)
	h.handshake(ctx)
	if configured := h.configure(ctx, "", nil); !configured.Ok {
		t.Fatalf("an empty configuration is {}: %v", configured)
	}
	if section := read(h.seen, func() string { return string(h.seen.configured.Section) }); section != "{}" {
		t.Errorf("an empty configuration reaches the hook as {}, got %q", section)
	}

	for _, section := range []string{"[]", `{"a":`, `"text"`, "null", "1", `{"a":1,"a":2}`, "\uFEFF{}", "{} {}"} {
		t.Run(section, func(t *testing.T) {
			h := start(t, defaultEnv())
			ctx := deadline(t)
			h.handshake(ctx)
			configured := h.configure(ctx, section, nil)
			expectStatus(t, "Configure with "+section, configured, ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "configure", "reason": "the configuration is not a JSON object"})
			if read(h.seen, func() Configuration { return h.seen.configured }).Section != nil {
				t.Error("the configure hook must not run for a configuration that is not an object")
			}
			if health := h.health(ctx); health.State != stateOf(primitives.Failed) || health.Error.GetMessageKey() != MessageUnexpected {
				t.Errorf("the plugin must be Failed with the error, got %v", health)
			}
			stopped, err := h.client.Stop(ctx, &pluginv1.StopRequest{Reason: "unload"})
			if err != nil || !stopped.Status.Ok {
				t.Fatalf("stop %v %v", stopped, err)
			}
			if err := h.exited(5 * time.Second); err != nil {
				t.Errorf("serve returned %v", err)
			}
			if stops := read(h.seen, func() []string { return h.seen.stops }); len(stops) != 0 {
				t.Errorf("the stop hook must not run when Start never succeeded, got %v", stops)
			}
		})
	}
}

// pathError 是测试里的一个自定义错误类型，按值实现 error。
type pathError struct{}

// Error 实现 error。
func (pathError) Error() string { return "C:\\secret\\connection-string" }

// genericError 是带类型参数的错误：类型名里的实参带完整的包路径，reason 只取简单名。
type genericError[T any] struct{ _ T }

// Error 实现 error。
func (e *genericError[T]) Error() string { return "generic" }

// TestHookFailuresFollowTheReasonRule 核对 lifecycle.md §7.2、§7.3、§7.6：回调返回 *primitives.Error 原样带出；返回别的 error 是抛出，
// reason 是动态类型的简单名，panic 写 panic；错误的文本只进 stderr，不进错误；都进入 Failed。
func TestHookFailuresFollowTheReasonRule(t *testing.T) {
	own := primitives.Validation("PROBE_VALIDATION_002", "probe.configuration.invalid", map[string]any{"field": "greeting"})
	cases := []struct {
		name    string
		fail    func() error
		code    string
		key     string
		details func(phase string) map[string]any
	}{
		{"primitives error", func() error { return own }, "PROBE_VALIDATION_002", "probe.configuration.invalid", func(string) map[string]any { return map[string]any{"field": "greeting"} }},
		// 用 %w 包了一层上下文的 *primitives.Error 仍是回调返回的失败，原样带出，不按抛出处理
		{"wrapped primitives error", func() error { return fmt.Errorf("load settings: %w", own) }, "PROBE_VALIDATION_002", "probe.configuration.invalid", func(string) map[string]any { return map[string]any{"field": "greeting"} }},
		{"path error", func() error { return &fs.PathError{Op: "open", Path: "C:\\secret", Err: errors.New("denied")} }, ErrorCodeUnexpected, MessageUnexpected, func(phase string) map[string]any {
			return map[string]any{"id": probeID, "phase": phase, "reason": "PathError"}
		}},
		{"value error", func() error { return pathError{} }, ErrorCodeUnexpected, MessageUnexpected, func(phase string) map[string]any {
			return map[string]any{"id": probeID, "phase": phase, "reason": "pathError"}
		}},
		{"errors.New", func() error { return errors.New("C:\\secret") }, ErrorCodeUnexpected, MessageUnexpected, func(phase string) map[string]any {
			return map[string]any{"id": probeID, "phase": phase, "reason": "errorString"}
		}},
		{"generic error", func() error { return &genericError[invocation.Context]{} }, ErrorCodeUnexpected, MessageUnexpected, func(phase string) map[string]any {
			return map[string]any{"id": probeID, "phase": phase, "reason": "genericError"}
		}},
		{"panic", func() error { panic("C:\\secret") }, ErrorCodeUnexpected, MessageUnexpected, func(phase string) map[string]any {
			return map[string]any{"id": probeID, "phase": phase, "reason": "panic"}
		}},
	}
	for _, c := range cases {
		for _, phase := range []string{"configure", "start"} {
			t.Run(c.name+"/"+phase, func(t *testing.T) {
				h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
					if phase == "configure" {
						opts.Hooks.Configure = func(context.Context, Configuration) error { return c.fail() }
					} else {
						opts.Hooks.Start = func(context.Context) error { return c.fail() }
					}
				})
				ctx := deadline(t)
				h.handshake(ctx)
				outcome := h.configure(ctx, "{}", nil)
				if phase == "start" {
					if !outcome.Ok {
						t.Fatalf("configure %v", outcome)
					}
					started, err := h.client.Start(ctx, &pluginv1.StartRequest{})
					if err != nil {
						t.Fatal(err)
					}
					outcome = started.Status
				}
				expectStatus(t, c.name, outcome, c.code, c.key, c.details(phase))
				if health := h.health(ctx); health.State != stateOf(primitives.Failed) {
					t.Errorf("the plugin must be Failed, got %v", health.State)
				}
				if c.key == MessageUnexpected && strings.Contains(outcome.GetError().GetDetails(), "secret") {
					t.Errorf("the error text must not reach the host: %s", outcome.GetError().GetDetails())
				}
			})
		}
	}
}

// TestStartChecksHealthBeforeAnswering 核对 lifecycle.md §7.3：Start 成功后立即求一次健康，按结果进入 Ready、Degraded 或 Failed，
// 然后才应答；报其他状态按 Ready；健康回调 panic 时是 Degraded；Failed 不可服务。
func TestStartChecksHealthBeforeAnswering(t *testing.T) {
	cases := []struct {
		name   string
		health func() Health
		want   primitives.LifecycleState
	}{
		{"ready", func() Health { return Health{State: primitives.Ready} }, primitives.Ready},
		{"degraded", func() Health { return Health{State: primitives.Degraded} }, primitives.Degraded},
		{"failed", func() Health { return Health{State: primitives.Failed} }, primitives.Failed},
		{"other state", func() Health { return Health{State: primitives.Started} }, primitives.Ready},
		{"panic", func() Health { panic("boom") }, primitives.Degraded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
				opts.Hooks.Health = func() Health { calls.Add(1); return c.health() }
			})
			ctx := deadline(t)
			h.ready(ctx, nil)
			if calls.Load() != 1 {
				t.Errorf("Start must call the health hook once before answering, called %d times", calls.Load())
			}
			health := h.health(ctx)
			if health.State != stateOf(c.want) {
				t.Errorf("state after Start %s, want %s", health.State, c.want)
			}
			invoked, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{"text":"x"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if c.want == primitives.Failed {
				expectStatus(t, "an invoke after a failed health check", invoked.Status, ErrorCodeUnavailable, MessageUnavailable, map[string]any{"id": probeID, "state": "Failed"})
				// Failed 之后不再调健康回调
				if calls.Load() != 1 {
					t.Errorf("the health hook must not run after Failed, called %d times", calls.Load())
				}
			} else if !invoked.Status.Ok {
				t.Errorf("a %s plugin serves calls, got %v", c.want, invoked.Status)
			}
		})
	}
}

// TestHealthFollowsTheHook 核对 lifecycle.md §7.4：可服务期间每次 Health 求一次回调；Ready、Degraded、Failed 改变状态，Failed 不再回来；
// 未声明的健康项丢掉、同名只记一条 warning；回调 panic 时这次是 Degraded、error 为 plugins.process.failed（phase 为 health）、没有健康项。
func TestHealthFollowsTheHook(t *testing.T) {
	var reported atomic.Value
	reported.Store(func() Health {
		return Health{State: primitives.Ready, Items: []HealthItem{{Name: "echo", Status: Healthy}, {Name: "undeclared", Status: Healthy}}}
	})
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
		opts.Hooks.Health = func() Health { return reported.Load().(func() Health)() }
	})
	ctx := deadline(t)
	h.ready(ctx, nil)
	for range 3 {
		if health := h.health(ctx); health.State != stateOf(primitives.Ready) || len(health.Items) != 1 || health.Items[0].Name != "echo" {
			t.Errorf("health %v", health)
		}
	}
	if warnings := strings.Count(h.stderr.String(), `"item":"undeclared"`); warnings != 1 {
		t.Errorf("an undeclared health item is warned about once, got %d warnings", warnings)
	}

	reported.Store(func() Health {
		return Health{State: primitives.Degraded, Error: primitives.Unavailable("PROBE_UNAVAILABLE_001", "probe.backend.down", nil)}
	})
	if health := h.health(ctx); health.State != stateOf(primitives.Degraded) || health.Error.GetCode() != "PROBE_UNAVAILABLE_001" || len(health.Items) != 0 {
		t.Errorf("degraded %v", health)
	}
	// 从 Ready 起回调 panic：这一次的结果是 Degraded，状态也迁到 Degraded（应答报的就是迁移之后的状态，lifecycle.md §7.4）
	reported.Store(func() Health { return Health{State: primitives.Ready} })
	if health := h.health(ctx); health.State != stateOf(primitives.Ready) {
		t.Errorf("ready again %v", health)
	}
	reported.Store(func() Health { panic("boom") })
	health := h.health(ctx)
	if health.State != stateOf(primitives.Degraded) || len(health.Items) != 0 {
		t.Errorf("a panicking health hook makes this result Degraded without items, got %v", health)
	}
	expectStatus(t, "a panicking health hook", &pluginv1.Status{Error: health.Error}, ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "health", "reason": "panic"})
	reported.Store(func() Health { return Health{State: primitives.Ready} })
	if health := h.health(ctx); health.State != stateOf(primitives.Ready) || health.Error != nil {
		t.Errorf("back to ready %v", health)
	}
	reported.Store(func() Health { return Health{State: primitives.Failed} })
	if health := h.health(ctx); health.State != stateOf(primitives.Failed) {
		t.Errorf("failed %v", health)
	}
	reported.Store(func() Health { return Health{State: primitives.Ready} })
	if health := h.health(ctx); health.State != stateOf(primitives.Failed) {
		t.Errorf("Failed does not come back, got %v", health)
	}
}

// TestWatchHealthPushesChanges 核对 lifecycle.md §7.4：先推当前状态，可服务期间每隔一段时间（缺省 10 秒）求一次回调、变化时推送，
// 进入 Draining 时推一次，应答 Stop 后结束流。
func TestWatchHealthPushesChanges(t *testing.T) {
	saved := healthInterval
	healthInterval = 20 * time.Millisecond
	t.Cleanup(func() { healthInterval = saved })
	var degraded atomic.Bool
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
		opts.Hooks.Health = func() Health {
			if degraded.Load() {
				return Health{State: primitives.Degraded}
			}
			return Health{State: primitives.Ready, Items: []HealthItem{{Name: "echo", Status: Healthy}}}
		}
	})
	ctx := deadline(t)

	// Start 之前推本地状态
	watch, err := h.client.WatchHealth(ctx, &pluginv1.HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if first, err := watch.Recv(); err != nil || first.State != stateOf(primitives.Declared) {
		t.Fatalf("the first push is the current state, got %v %v", first, err)
	}
	h.ready(ctx, nil)
	expectNext := func(want primitives.LifecycleState) {
		t.Helper()
		for {
			update, err := watch.Recv()
			if err != nil {
				t.Fatalf("waiting for %s: %v", want, err)
			}
			if update.State == stateOf(want) {
				return
			}
		}
	}
	expectNext(primitives.Ready)
	// 没有 Health 调用，周期性地求回调发现了变化
	degraded.Store(true)
	expectNext(primitives.Degraded)

	stopped, err := h.client.Stop(ctx, &pluginv1.StopRequest{GraceSeconds: 1, Reason: "unload"})
	if err != nil || !stopped.Status.Ok {
		t.Fatalf("stop %v %v", stopped, err)
	}
	expectNext(primitives.Draining)
	if update, err := watch.Recv(); err == nil {
		t.Errorf("the stream must end after Stop, got %v", update)
	}
}

// blockingCall 让 probes.run 等到调用的取消信号；ignore 为真时取消之后也不返回，直到 release 关闭。
func blockingCall(entered chan<- struct{}, cancelled chan<- time.Time, ignore bool, release <-chan struct{}) func(ctx *invocation.Context) *primitives.Error {
	return func(ctx *invocation.Context) *primitives.Error {
		close(entered)
		<-ctx.Done()
		cancelled <- time.Now()
		if ignore {
			<-release
		}
		return nil
	}
}

// TestStopWaitsForInFlightCalls 核对 lifecycle.md §7.5：进入 Draining、拒绝新调用、等在途调用到宽限，到时触发取消信号不再等，
// 再调停止回调、应答、退出；停止中再 Stop 以 FailedPrecondition 拒绝。
func TestStopWaitsForInFlightCalls(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan time.Time, 1), make(chan struct{})
	t.Cleanup(func() { close(release) })
	hooked := make(chan time.Time, 1)
	h := launch(t, defaultEnv(), func(opts *Options, seen *recorder) {
		seen.run = blockingCall(entered, cancelled, true, release)
		opts.Hooks.Stop = func(_ context.Context, reason string, grace time.Duration) error {
			hooked <- time.Now()
			return nil
		}
	})
	ctx := deadline(t)
	h.ready(ctx, nil)
	go func() { _, _ = h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "probes.run"}) }()
	<-entered

	begun := time.Now()
	answered := make(chan *pluginv1.StopResponse, 1)
	go func() {
		response, err := h.client.Stop(ctx, &pluginv1.StopRequest{GraceSeconds: 1, Reason: "unload"})
		if err != nil {
			t.Errorf("stop: %v", err)
		}
		answered <- response
	}()
	// 停止中：新调用以 plugins.process.unavailable（Draining）拒绝，第二次 Stop 以 FailedPrecondition 拒绝
	for h.health(ctx).State != stateOf(primitives.Draining) {
		time.Sleep(5 * time.Millisecond)
	}
	invoked, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{"text":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "an invoke while draining", invoked.Status, ErrorCodeUnavailable, MessageUnavailable, map[string]any{"id": probeID, "state": "Draining"})
	if _, err := h.client.Stop(ctx, &pluginv1.StopRequest{Reason: "unload"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("a Stop while stopping must be refused, got %v", err)
	}

	cancelledAt := <-cancelled
	if elapsed := cancelledAt.Sub(begun); elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("the in-flight call must be cancelled when the 1 second grace ends, cancelled after %s", elapsed)
	}
	// 不理会取消信号的调用不再等：停止回调在取消之后就调用
	if hookedAt := <-hooked; hookedAt.Before(cancelledAt.Add(-50 * time.Millisecond)) {
		t.Errorf("the stop hook ran before the grace ended")
	}
	if response := <-answered; !response.Status.Ok {
		t.Errorf("stop %v", response.Status)
	}
}

// TestStopHookRunsOnceAndOnlyAfterStart 核对 lifecycle.md §7.5：启动回调没成功过不调停止回调；启动成功过、之后 Failed 的照样调；
// 停止回调失败写进 StopResponse.status，照常退出。
func TestStopHookRunsOnceAndOnlyAfterStart(t *testing.T) {
	t.Run("before start", func(t *testing.T) {
		h := start(t, defaultEnv())
		ctx := deadline(t)
		h.handshake(ctx)
		h.configure(ctx, "{}", nil)
		stopped, err := h.client.Stop(ctx, &pluginv1.StopRequest{Reason: "unload"})
		if err != nil || !stopped.Status.Ok {
			t.Fatalf("stop %v %v", stopped, err)
		}
		if err := h.exited(5 * time.Second); err != nil {
			t.Errorf("serve returned %v", err)
		}
		if stops := read(h.seen, func() []string { return h.seen.stops }); len(stops) != 0 {
			t.Errorf("the stop hook must not run, got %v", stops)
		}
	})
	t.Run("failed after start", func(t *testing.T) {
		h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
			opts.Hooks.Health = func() Health { return Health{State: primitives.Failed} }
		})
		ctx := deadline(t)
		h.ready(ctx, nil)
		stopped, err := h.client.Stop(ctx, &pluginv1.StopRequest{Reason: "restart"})
		if err != nil || !stopped.Status.Ok {
			t.Fatalf("stop %v %v", stopped, err)
		}
		if stops := read(h.seen, func() []string { return h.seen.stops }); !reflect.DeepEqual(stops, []string{"restart"}) {
			t.Errorf("the stop hook must run once, got %v", stops)
		}
	})
	t.Run("failing hook", func(t *testing.T) {
		h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
			opts.Hooks.Stop = func(context.Context, string, time.Duration) error { return errors.New("flush failed") }
		})
		ctx := deadline(t)
		h.ready(ctx, nil)
		stopped, err := h.client.Stop(ctx, &pluginv1.StopRequest{Reason: "unload"})
		if err != nil {
			t.Fatal(err)
		}
		expectStatus(t, "a failing stop hook", stopped.Status, ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "stop", "reason": "errorString"})
		if err := h.exited(5 * time.Second); err != nil {
			t.Errorf("serve returned %v", err)
		}
	})
}

// TestStdinCloseShutsDown 核对 lifecycle.md §7.7：stdin 读到结尾时可服务的进入 Draining，立即触发在途调用的取消信号并等它们，
// 调一次停止回调（reason 为 shutdown），以 nil 返回；不理会取消的调用最多等 3 秒。
func TestStdinCloseShutsDown(t *testing.T) {
	t.Run("cancels in-flight calls", func(t *testing.T) {
		entered, cancelled := make(chan struct{}), make(chan time.Time, 1)
		h := launch(t, defaultEnv(), func(_ *Options, seen *recorder) {
			seen.run = blockingCall(entered, cancelled, false, nil)
		})
		ctx := deadline(t)
		h.ready(ctx, nil)
		go func() { _, _ = h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "probes.run"}) }()
		<-entered
		closing := time.Now()
		_ = h.stdin.Close()
		if cancelledAt := <-cancelled; cancelledAt.Sub(closing) > time.Second {
			t.Errorf("the in-flight call must be cancelled at once, took %s", cancelledAt.Sub(closing))
		}
		if err := h.exited(5 * time.Second); err != nil {
			t.Errorf("serve returned %v", err)
		}
		if stops := read(h.seen, func() []string { return h.seen.stops }); !reflect.DeepEqual(stops, []string{"shutdown"}) {
			t.Errorf("the stop hook must run once with reason shutdown, got %v", stops)
		}
	})
	t.Run("before start", func(t *testing.T) {
		h := start(t, defaultEnv())
		h.handshake(deadline(t))
		_ = h.stdin.Close()
		if err := h.exited(5 * time.Second); err != nil {
			t.Errorf("serve returned %v", err)
		}
		if stops := read(h.seen, func() []string { return h.seen.stops }); len(stops) != 0 {
			t.Errorf("the stop hook must not run when Start never succeeded, got %v", stops)
		}
	})
	t.Run("at most three seconds", func(t *testing.T) {
		entered, cancelled, release := make(chan struct{}), make(chan time.Time, 1), make(chan struct{})
		t.Cleanup(func() { close(release) })
		h := launch(t, defaultEnv(), func(_ *Options, seen *recorder) {
			seen.run = blockingCall(entered, cancelled, true, release)
		})
		ctx := deadline(t)
		h.ready(ctx, nil)
		go func() { _, _ = h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "probes.run"}) }()
		<-entered
		closing := time.Now()
		_ = h.stdin.Close()
		if err := h.exited(6 * time.Second); err != nil {
			t.Errorf("serve returned %v", err)
		}
		if elapsed := time.Since(closing); elapsed < 2500*time.Millisecond || elapsed > 4500*time.Millisecond {
			t.Errorf("a call that ignores its cancellation holds the shutdown for at most 3 seconds, took %s", elapsed)
		}
	})
}

// TestContextDoneShutsDown 核对 Serve 的上下文结束与 stdin 关闭一样收尾（lifecycle.md §7.7）。
func TestContextDoneShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	seen := &recorder{}
	opts := probeOptions(t, seen)
	env := defaultEnv()
	stdinReader, stdinWriter := io.Pipe()
	defer stdinWriter.Close()
	opts.Stdin, opts.Stdout, opts.Stderr = stdinReader, io.Discard, io.Discard
	opts.Getenv = func(key string) string { return env[key] }
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, opts) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve must return after its context ends")
	}
}
