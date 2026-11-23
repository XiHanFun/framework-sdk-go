package plugin

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// divertDefaultLogger 在测试期间把 slog.Default 换成写进缓冲的日志器，返回那个缓冲：运行时与调用器都不该往它里面写
func divertDefaultLogger(t *testing.T) *syncBuffer {
	t.Helper()
	fallback := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(fallback, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return fallback
}

// panicking 让 probes.run 以 panic 结束
func panicking(seen *recorder) {
	seen.run = func(*invocation.Context) *primitives.Error { panic("secret detail") }
}

// invokePanicking 调一次会 panic 的 probes.run，核对它翻译成未处理的异常（RFC-0011 规则 5）
func invokePanicking(t *testing.T, h *harness) {
	t.Helper()
	ctx := deadline(t)
	h.ready(ctx, nil)
	invoked, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "probes.run"})
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "a panicking operation", invoked.Status, operation.ErrorCodeUnexpected, invocation.MessageUnhandledException, nil)
}

// TestOperationPanicsAreLoggedByTheRuntime 核对运行时把自己的日志器交给调用器：操作 panic 的原因写进运行时的 JSON 行日志
// （Stderr，带 plugin），宿主读得到；不写进 slog.Default
func TestOperationPanicsAreLoggedByTheRuntime(t *testing.T) {
	fallback := divertDefaultLogger(t)
	h := launch(t, defaultEnv(), func(_ *Options, seen *recorder) { panicking(seen) })
	invokePanicking(t, h)
	var line string
	for _, candidate := range strings.Split(h.stderr.String(), "\n") {
		if strings.Contains(candidate, "an operation panicked") {
			line = candidate
		}
	}
	for _, part := range []string{`"level":"error"`, `"message":"an operation panicked"`, `"plugin":"` + probeID + `"`, `"operation":"probes.run"`, `"panic":"secret detail"`} {
		if !strings.Contains(line, part) {
			t.Errorf("the runtime log line must contain %s, got %.300q", part, line)
		}
	}
	if written := fallback.String(); written != "" {
		t.Errorf("nothing may be written to slog.Default, got %.300q", written)
	}
}

// TestServeUsesTheGivenLogger 核对 Options.Logger：给了就用它写运行时与调用器的日志（运行时照样加上 plugin），不再往 Stderr 写
func TestServeUsesTheGivenLogger(t *testing.T) {
	fallback := divertDefaultLogger(t)
	custom := &syncBuffer{}
	h := launch(t, defaultEnv(), func(opts *Options, seen *recorder) {
		opts.Logger = slog.New(slog.NewTextHandler(custom, nil)).With("component", "probe")
		panicking(seen)
	})
	invokePanicking(t, h)
	logs := custom.String()
	for _, part := range []string{"msg=listening", `msg="handshake completed"`, `msg="an operation panicked"`, "operation=probes.run", "component=probe", "plugin=" + probeID} {
		if !strings.Contains(logs, part) {
			t.Errorf("the given logger must receive %s, got %.300q", part, logs)
		}
	}
	if written := h.stderr.String(); written != "" {
		t.Errorf("nothing may be written to Stderr when a logger is given, got %q", written)
	}
	if written := fallback.String(); written != "" {
		t.Errorf("nothing may be written to slog.Default, got %.300q", written)
	}
}
