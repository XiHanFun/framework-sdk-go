package operation_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// marker 是记下自己被执行过的中间件
func marker(name string, log *[]string) pipeline.Middleware {
	return pipeline.Func(name, func(ctx *invocation.Context, next pipeline.Step) primitives.Result[primitives.Unit] {
		*log = append(*log, name)
		return next(ctx)
	})
}

// TestBuiltInStepsSitAtTheBeginningOfTheirStages 核对三个内置步骤的位置（RFC-0011 规则 10，ADR-0236）：都按归属 xihan.kernel.core
// 放在所在阶段的 Beginning，与同位置的其他中间件按归属的序数序排序；传入的构建器本身不变
func TestBuiltInStepsSitAtTheBeginningOfTheirStages(t *testing.T) {
	var log []string
	builder := pipeline.NewBuilder().
		UseAt(marker("AppObserve", &log), primitives.Observe, pipeline.Beginning, "app").
		UseAt(marker("LateObserve", &log), primitives.Observe, pipeline.Beginning, "xihan.zeta").
		UseAt(marker("Retry", &log), primitives.Resilience, pipeline.Beginning, "app").
		UseAt(marker("Check", &log), primitives.Validate, pipeline.End, "app")
	invoker := newInvoker(t, builder)
	descriptors, err := invoker.Pipeline()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, descriptor := range descriptors {
		names = append(names, descriptor.Stage.ManifestName()+":"+descriptor.Middleware)
	}
	want := "observe:AppObserve,observe:ObserveMiddleware,observe:LateObserve,resilience:Retry,resilience:DeadlineMiddleware,validate:ValidationMiddleware,validate:Check"
	if strings.Join(names, ",") != want {
		t.Errorf("pipeline %s\nwant %s", strings.Join(names, ","), want)
	}
	if own, _ := builder.Describe(); len(own) != 4 {
		t.Errorf("the builder passed in is not changed: %v", own)
	}
	if response := invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":1}`)}); !response.Outcome.OK() {
		t.Fatal(response.Outcome)
	}
	if strings.Join(log, ",") != "AppObserve,LateObserve,Retry,Check" {
		t.Errorf("executed %v", log)
	}
}

// TestFailuresAreReportedInPipelineOrder 核对几种失败同时成立时按位置先后报第一个：未登记与类型不符在进管道前，截止时间在 Resilience，
// 解码在 Validate，所以 Authorize 拒绝先于载荷不合法，截止时间已到先于两者
func TestFailuresAreReportedInPipelineOrder(t *testing.T) {
	denied := primitives.Denied("ORDERS_DENIED_001", "orders.denied", nil)
	builder := pipeline.NewBuilder().UseAt(pipeline.Func("Deny", func(*invocation.Context, pipeline.Step) primitives.Result[primitives.Unit] {
		return primitives.Fail[primitives.Unit](denied)
	}), primitives.Authorize, pipeline.End, "app")
	invoker := newInvoker(t, builder)
	bad := operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":"x"}`)}
	if err := invoker.Invoke(invocation.Background(), bad).Outcome.Err(); err == nil || err.Code != "ORDERS_DENIED_001" {
		t.Errorf("authorization comes before decoding: %v", err)
	}
	expired := invocation.Background(invocation.WithDeadline(time.Now().Add(-time.Second)))
	if err := invoker.Invoke(expired, bad).Outcome.Err(); err == nil || err.Code != "KERNEL_TIMEOUT_001" || err.MessageKey != invocation.MessageDeadlineBeforeStart {
		t.Errorf("the deadline comes before authorization: %v", err)
	}
	if err := invoker.Invoke(expired, operation.Request{ID: "orders.missing"}).Outcome.Err(); err == nil || err.Code != operation.ErrorCodeNotFound {
		t.Errorf("an unknown operation comes first: %v", err)
	}
	if err := operation.Invoke[createOrder, string](invoker, expired, "orders.create", createOrder{}).Err(); err == nil || err.Code != operation.ErrorCodeTypeMismatch {
		t.Errorf("a type mismatch comes before the pipeline: %v", err)
	}
	plain := newInvoker(t, nil)
	if err := plain.Invoke(invocation.Background(), bad).Outcome.Err(); err == nil || err.Code != operation.ErrorCodeInvalidPayload {
		t.Errorf("without a denial the payload is decoded in Validate: %v", err)
	}
}

// TestPanicsBeforeTheObserveStepReachTheCaller 核对只有排在异常翻译之后的 panic 被翻译：Ingress 阶段与 Observe 阶段 Beginning 上
// 归属排在 xihan.kernel.core 之前的中间件的 panic 原样交给调用方（与 .NET 一致）
func TestPanicsBeforeTheObserveStepReachTheCaller(t *testing.T) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	explode := pipeline.Func("Explode", func(*invocation.Context, pipeline.Step) primitives.Result[primitives.Unit] { panic("early") })
	for name, builder := range map[string]*pipeline.Builder{
		"ingress":                 pipeline.NewBuilder().UseAt(explode, primitives.Ingress, pipeline.End, "app"),
		"observe before the core": pipeline.NewBuilder().UseAt(explode, primitives.Observe, pipeline.Beginning, "app"),
	} {
		invoker := newInvoker(t, builder)
		func() {
			defer func() {
				if recovered := recover(); recovered != "early" {
					t.Errorf("%s: the panic reaches the caller, got %v", name, recovered)
				}
			}()
			invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":1}`)})
			t.Errorf("%s: Invoke must panic", name)
		}()
	}
	late := newInvoker(t, pipeline.NewBuilder().UseAt(explode, primitives.Observe, pipeline.Beginning, "xihan.zeta"))
	if err := late.Invoke(invocation.Background(), operation.Request{ID: "orders.create"}).Outcome.Err(); err == nil || err.MessageKey != invocation.MessageUnhandledException {
		t.Errorf("a panic after the observe step is translated: %v", err)
	}
}
