package operation_test

import (
	"io"
	"log/slog"
	"math"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

type measurement struct {
	Value float64 `json:"value"`
}

// misconfigured 的 MarshalJSON 返回框架错误
type misconfigured struct{}

func (misconfigured) MarshalJSON() ([]byte, error) {
	return nil, &primitives.FrameworkError{Code: "ORDERS_VALIDATION_009", Message: "Misconfigured."}
}

// exploding 的 MarshalJSON panic
type exploding struct{}

func (exploding) MarshalJSON() ([]byte, error) { panic("boom") }

// register 登记一个返回固定响应的操作
func register[Res any](t *testing.T, registry *operation.Registry, id string, response Res) {
	t.Helper()
	if err := operation.Add(registry, operation.Contract{ID: id}, operation.HandlerFunc[struct{}, Res](func(*invocation.Context, struct{}) primitives.Result[Res] {
		return primitives.Ok(response)
	}), nil, ""); err != nil {
		t.Fatal(err)
	}
}

// TestResponsesAreEncodedAfterThePipeline 核对 RFC-0011 规则 10「编码」（ADR-0236）：XGI 入口在管道结束之后才编码响应，中间件看到的是
// 编码之前的对象；写不出时（浮点是 NaN 或无穷、序列化不支持的值）以 KERNEL_UNEXPECTED_001 + unhandled_exception 结束、没有载荷、
// 不抛给调用方，框架错误放进 inner；类型化入口不编码
func TestResponsesAreEncodedAfterThePipeline(t *testing.T) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	registry := operation.NewRegistry()
	register(t, registry, "measurements.get", measurement{Value: 1.5})
	register(t, registry, "measurements.nan", measurement{Value: math.NaN()})
	register(t, registry, "measurements.infinite", measurement{Value: math.Inf(1)})
	register(t, registry, "measurements.channel", map[string]any{"c": make(chan int)})
	register(t, registry, "measurements.misconfigured", misconfigured{})
	register(t, registry, "measurements.exploding", exploding{})

	var seen []any
	builder := pipeline.NewBuilder().UseAt(pipeline.Func("Inspect", func(ctx *invocation.Context, next pipeline.Step) primitives.Result[primitives.Unit] {
		outcome := next(ctx)
		if response, ok := operation.ResponseOf(ctx); ok {
			seen = append(seen, response)
		}
		return outcome
	}), primitives.Egress, pipeline.End, "app")
	invoker, err := operation.NewInvoker(registry, builder)
	if err != nil {
		t.Fatal(err)
	}
	ok := invoker.Invoke(invocation.Background(), operation.Request{ID: "measurements.get"})
	if !ok.Outcome.OK() || string(ok.Payload) != `{"value":1.5}` {
		t.Fatalf("response %v %s", ok.Outcome, ok.Payload)
	}
	if len(seen) != 1 || seen[0] != (measurement{Value: 1.5}) {
		t.Errorf("middleware sees the response object before encoding: %#v", seen)
	}
	for _, id := range []string{"measurements.nan", "measurements.infinite", "measurements.channel", "measurements.exploding"} {
		response := invoker.Invoke(invocation.Background(), operation.Request{ID: id})
		assertError(t, response.Outcome.Err(), "KERNEL_UNEXPECTED_001", "kernel.invocation.unhandled_exception", nil, "An unhandled exception occurred during the invocation.")
		if response.Payload != nil || response.Outcome.Err().Inner != nil {
			t.Errorf("%s: no payload and no inner error, got %s %v", id, response.Payload, response.Outcome.Err().Inner)
		}
	}
	misconfiguredResponse := invoker.Invoke(invocation.Background(), operation.Request{ID: "measurements.misconfigured"})
	if inner := misconfiguredResponse.Outcome.Err().Inner; inner == nil || inner.Code != "ORDERS_VALIDATION_009" {
		t.Errorf("a FrameworkError from MarshalJSON becomes the inner error: %v", misconfiguredResponse.Outcome.Err())
	}
	descriptor, _ := registry.Get("measurements.nan")
	if executed := descriptor.Execute(invocation.Background(), nil); executed.Outcome.Err() == nil || executed.Outcome.Err().MessageKey != invocation.MessageUnhandledException {
		t.Errorf("Execute encodes the same way: %v", executed.Outcome)
	}
	if typed := operation.Invoke[struct{}, measurement](invoker, invocation.Background(), "measurements.nan", struct{}{}); !typed.OK() || !math.IsNaN(typed.Must().Value) {
		t.Errorf("the typed entry point does not encode: %v", typed)
	}
}
