package operation_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestPanicsBecomeUnexpectedResults 核对 RFC-0011 规则 5 与规则 10：处理器或中间件的 panic 翻译为 KERNEL_UNEXPECTED_001
// （kernel.invocation.unhandled_exception，没有参数），panic 的值只进日志；值是 FrameworkError 时作为内部错误带上
func TestPanicsBecomeUnexpectedResults(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	registry := operation.NewRegistry()
	boom := operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { panic("secret detail") })
	framework := operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] {
		panic(&primitives.FrameworkError{Code: "ORDERS_VALIDATION_009", Message: "Misconfigured."})
	})
	fine := operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { return primitives.Ok(struct{}{}) })
	for id, handler := range map[string]operation.HandlerFunc[struct{}, struct{}]{"jobs.boom": boom, "jobs.misconfigured": framework, "jobs.fine": fine} {
		if err := operation.Add(registry, operation.Contract{ID: id}, handler, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	invoker, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := invocation.Background()
	response := invoker.Invoke(ctx, operation.Request{ID: "jobs.boom"})
	assertError(t, response.Outcome.Err(), "KERNEL_UNEXPECTED_001", "kernel.invocation.unhandled_exception", nil, "An unhandled exception occurred during the invocation.")
	if strings.Contains(response.Outcome.Err().Message(), "secret") || !strings.Contains(logs.String(), "secret detail") || !strings.Contains(logs.String(), "jobs.boom") {
		t.Errorf("the panic value goes to the log only: %s", logs.String())
	}
	typed := operation.Invoke[struct{}, struct{}](invoker, ctx, "jobs.misconfigured", struct{}{})
	assertError(t, typed.Err(), "KERNEL_UNEXPECTED_001", "kernel.invocation.unhandled_exception", nil, "An unhandled exception occurred during the invocation.")
	if inner := typed.Err().Inner; inner == nil || inner.Code != "ORDERS_VALIDATION_009" || inner.Message() != "Misconfigured." {
		t.Errorf("a FrameworkError becomes the inner error: %v", inner)
	}
	if after := invoker.Invoke(ctx, operation.Request{ID: "jobs.fine"}); !after.Outcome.OK() {
		t.Errorf("the context is usable after a panic: %v", after.Outcome)
	}

	middleware := pipeline.NewBuilder().UseAt(pipeline.Func("Explode", func(*invocation.Context, pipeline.Step) primitives.Result[primitives.Unit] {
		panic("middleware")
	}), primitives.Observe, pipeline.End, "")
	exploding, err := operation.NewInvoker(registry, middleware)
	if err != nil {
		t.Fatal(err)
	}
	assertError(t, exploding.Invoke(ctx, operation.Request{ID: "jobs.fine"}).Outcome.Err(), "KERNEL_UNEXPECTED_001", "kernel.invocation.unhandled_exception", nil, "An unhandled exception occurred during the invocation.")
}
