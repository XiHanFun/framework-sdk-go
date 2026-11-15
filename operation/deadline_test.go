package operation_test

import (
	"context"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestDeadlineBeforeStartSkipsThePipeline 核对调用开始时截止时间已过：KERNEL_TIMEOUT_001 + kernel.invocation.deadline_before_start，不进管道
func TestDeadlineBeforeStartSkipsThePipeline(t *testing.T) {
	entered := false
	builder := pipeline.NewBuilder().UseAt(pipeline.Func("Observe", func(ctx *invocation.Context, next pipeline.Step) primitives.Result[primitives.Unit] {
		entered = true
		return next(ctx)
	}), primitives.Observe, pipeline.End, "")
	invoker := newInvoker(t, builder)
	expired := invocation.Background(invocation.WithDeadline(time.Now().Add(-time.Second)))

	response := invoker.Invoke(expired, operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":1}`)})
	assertError(t, response.Outcome.Err(), "KERNEL_TIMEOUT_001", "kernel.invocation.deadline_before_start", nil, "The invocation exceeded its deadline before it started.")
	typed := operation.Invoke[createOrder, orderCreated](invoker, expired, "orders.create", createOrder{Quantity: 1})
	assertError(t, typed.Err(), "KERNEL_TIMEOUT_001", "kernel.invocation.deadline_before_start", nil, "The invocation exceeded its deadline before it started.")
	if entered {
		t.Error("an expired invocation must not enter the pipeline")
	}
}

// waitingInvoker 登记 jobs.wait：等上下文结束，然后按 RFC-0004 的做法写下取消结果，交给调用器归因
func waitingInvoker(t *testing.T) *operation.Invoker {
	t.Helper()
	registry := operation.NewRegistry()
	wait := operation.HandlerFunc[struct{}, struct{}](func(ctx *invocation.Context, _ struct{}) primitives.Result[struct{}] {
		<-ctx.Done()
		return primitives.Fail[struct{}](primitives.Cancelled(invocation.ErrorCodeCancelled, invocation.MessageCancelled, nil))
	})
	if err := operation.Add(registry, operation.Contract{ID: "jobs.wait"}, wait, nil, ""); err != nil {
		t.Fatal(err)
	}
	invoker, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return invoker
}

// TestDownstreamCancellationIsRewrittenOnceTheDeadlineFired 核对下游写下 KERNEL_CANCELLED_001 而截止令牌已触发时改写为超时（ADR-0092）
func TestDownstreamCancellationIsRewrittenOnceTheDeadlineFired(t *testing.T) {
	invoker := waitingInvoker(t)

	timedOut := invoker.Invoke(invocation.Background(invocation.WithTimeout(20*time.Millisecond)), operation.Request{ID: "jobs.wait"})
	assertError(t, timedOut.Outcome.Err(), "KERNEL_TIMEOUT_001", "kernel.invocation.deadline_exceeded", nil, "The invocation exceeded its deadline.")

	parent, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	cancelled := operation.Invoke[struct{}, struct{}](invoker, invocation.New(parent, invocation.WithTimeout(time.Hour)), "jobs.wait", struct{}{})
	assertError(t, cancelled.Err(), "KERNEL_CANCELLED_001", "kernel.invocation.cancelled", nil, "The invocation was canceled.")

	withoutDeadline, stop := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, stop)
	plain := invoker.Invoke(invocation.New(withoutDeadline), operation.Request{ID: "jobs.wait"})
	assertError(t, plain.Outcome.Err(), "KERNEL_CANCELLED_001", "kernel.invocation.cancelled", nil, "The invocation was canceled.")
}
