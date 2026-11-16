package operation_test

import (
	"context"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestCancellationOnlyReachesHandlersThroughTheToken 核对 RFC-0003 规则 1（ADR-0236）：调用器与内置步骤不强行中止处理器，令牌触发后
// 处理器照常返回结果的，调用以那个结果结束
func TestCancellationOnlyReachesHandlersThroughTheToken(t *testing.T) {
	registry := operation.NewRegistry()
	if err := operation.Add(registry, operation.Contract{ID: "jobs.finish"}, operation.HandlerFunc[struct{}, measurement](func(ctx *invocation.Context, _ struct{}) primitives.Result[measurement] {
		<-ctx.Done()
		time.Sleep(5 * time.Millisecond)
		return primitives.Ok(measurement{Value: 2})
	}), nil, ""); err != nil {
		t.Fatal(err)
	}
	invoker, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	timedOut := invoker.Invoke(invocation.Background(invocation.WithTimeout(10*time.Millisecond)), operation.Request{ID: "jobs.finish"})
	if !timedOut.Outcome.OK() || string(timedOut.Payload) != `{"value":2}` {
		t.Errorf("a handler that returns after the deadline ends the call with its result: %v %s", timedOut.Outcome, timedOut.Payload)
	}
	parent, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	cancelled := operation.Invoke[struct{}, measurement](invoker, invocation.New(parent), "jobs.finish", struct{}{})
	if !cancelled.OK() || cancelled.Must().Value != 2 {
		t.Errorf("a handler that returns after cancellation ends the call with its result: %v", cancelled)
	}
}
