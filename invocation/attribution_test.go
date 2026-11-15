package invocation_test

import (
	"context"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
)

// TestCancellationIsAttributedByTheDeadlineToken 核对 RFC-0004 / ADR-0092 的归因：只看截止令牌，不按谁先发生判断
func TestCancellationIsAttributedByTheDeadlineToken(t *testing.T) {
	// 调用方先取消、随后到了截止时间：截止令牌触发前报取消，触发后报超时（嵌入的 Err() 仍是 Canceled）
	parent, cancel := context.WithCancel(context.Background())
	ctx := invocation.New(parent, invocation.WithTimeout(40*time.Millisecond))
	cancel()
	<-ctx.Done()
	if err := ctx.Cancellation(); err == nil || err.Code != invocation.ErrorCodeCancelled || ctx.DeadlineExceeded() {
		t.Errorf("before the deadline: %v (deadline exceeded %v)", err, ctx.DeadlineExceeded())
	}
	waitFor(t, ctx.DeadlineExceeded)
	if err := ctx.Cancellation(); err == nil || err.Code != invocation.ErrorCodeDeadlineExceeded || err.MessageKey != invocation.MessageDeadlineExceeded {
		t.Errorf("after the deadline: %v", err)
	}
	if ctx.Err() != context.Canceled {
		t.Errorf("the embedded error stays %v", ctx.Err())
	}
	if derived := ctx.WithBaggage("xihan.feature-flag", "on"); derived.Cancellation().Code != invocation.ErrorCodeDeadlineExceeded {
		t.Error("derived contexts share the deadline token")
	}

	// 截止时间先到、调用方随后取消：超时
	parent, cancel = context.WithCancel(context.Background())
	ctx = invocation.New(parent, invocation.WithTimeout(10*time.Millisecond))
	<-ctx.Done()
	cancel()
	if err := ctx.Cancellation(); err == nil || err.Code != invocation.ErrorCodeDeadlineExceeded || !ctx.DeadlineExceeded() {
		t.Errorf("deadline first: %v", err)
	}

	// 没有截止时间：取消就是取消
	parent, cancel = context.WithCancel(context.Background())
	ctx = invocation.New(parent)
	cancel()
	if err := ctx.Cancellation(); err == nil || err.Code != invocation.ErrorCodeCancelled || ctx.DeadlineExceeded() {
		t.Errorf("no deadline: %v", err)
	}

	// 上游 context 自己的截止时间（没有 WithDeadline）：嵌入的上下文以 DeadlineExceeded 结束即超时
	upstream, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	ctx = invocation.New(upstream)
	<-ctx.Done()
	if err := ctx.Cancellation(); err == nil || err.Code != invocation.ErrorCodeDeadlineExceeded {
		t.Errorf("upstream deadline: %v", err)
	}
	if invocation.Background(invocation.WithTimeout(time.Hour)).Cancellation() != nil {
		t.Error("an unfinished context has no cancellation")
	}
}

// waitFor 等条件成立，最多一秒
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("the condition did not hold in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
