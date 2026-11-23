package invocation_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// valueKey 是测试放进底层 context 的值的键
type valueKey struct{}

// TestWithContextSwapsTheUnderlyingContext 核对 WithContext：取消与 Value 改看新的底层 context，追踪、Baggage、对象袋与
// 这次调用的截止时间照旧；原来的上下文不变
func TestWithContextSwapsTheUnderlyingContext(t *testing.T) {
	const traceParent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"
	base := invocation.Background(
		invocation.WithTraceParent(traceParent),
		invocation.WithBaggage(map[string]string{primitives.BaggageTenant: "t1", "xihan.feature-flag": "on"}),
		invocation.WithTimeout(time.Hour),
	)
	base.Set("shared", 1)
	inner, cancel := context.WithCancel(context.WithValue(base, valueKey{}, "v"))
	defer cancel()
	derived := base.WithContext(inner)

	if derived.Value(valueKey{}) != "v" || base.Value(valueKey{}) != nil {
		t.Errorf("values come from the new context only: derived %v, base %v", derived.Value(valueKey{}), base.Value(valueKey{}))
	}
	if derived.TraceID() != base.TraceID() || derived.SpanID() != base.SpanID() || derived.TraceParent() != traceParent {
		t.Errorf("the trace is shared: %s", derived.TraceParent())
	}
	if flag, _ := derived.GetBaggage("xihan.feature-flag"); derived.Tenant() != "t1" || flag != "on" {
		t.Errorf("the baggage is shared: %v", derived.Baggage())
	}
	derived.Set("added", 2)
	if value, ok := derived.Get("shared"); !ok || value != 1 {
		t.Error("the derived context reads the shared items")
	}
	if value, ok := base.Get("added"); !ok || value != 2 {
		t.Error("items set on the derived context are visible on the original")
	}
	want, _ := base.Deadline()
	if got, ok := derived.Deadline(); !ok || !got.Equal(want) || !derived.HasDeadline() {
		t.Errorf("the deadline of the call is kept: %v, want %v", got, want)
	}

	cancel()
	if err := derived.Cancellation(); derived.Err() != context.Canceled || err == nil || err.Code != invocation.ErrorCodeCancelled {
		t.Errorf("the derived context follows the new context: %v (%v)", derived.Err(), err)
	}
	if base.Err() != nil || base.Cancellation() != nil {
		t.Errorf("the original context is untouched: %v", base.Err())
	}

	defer func() {
		var framework *primitives.FrameworkError
		if recovered, _ := recover().(error); !errors.As(recovered, &framework) || framework.Code != primitives.ErrorCodeInvalidArgument {
			t.Errorf("a nil context must panic with PRIMITIVES_VALIDATION_001, got %v", recovered)
		}
	}()
	var missing context.Context
	base.WithContext(missing)
}

// TestWithContextTakesTheEarlierDeadline 核对底层 context 自己的截止时间：比这次调用的更早（或调用没有截止时间）时派生上下文的
// 截止时间就是它，到了按超时归因；更晚的不放宽调用的截止时间
func TestWithContextTakesTheEarlierDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base, release := invocation.NewWithCancel(context.Background(), invocation.WithTimeout(time.Hour))
		defer release()
		tighter, cancel := context.WithTimeout(base, time.Minute)
		defer cancel()
		derived := base.WithContext(tighter)
		if got, ok := derived.Deadline(); !ok || !got.Equal(time.Now().Add(time.Minute)) {
			t.Errorf("the earlier deadline wins: %v", got)
		}
		if remaining, ok := derived.RemainingTime(time.Now()); !ok || remaining != time.Minute {
			t.Errorf("remaining %v", remaining)
		}
		if got, _ := base.Deadline(); !got.Equal(time.Now().Add(time.Hour)) {
			t.Errorf("the original keeps its deadline: %v", got)
		}

		looser, cancelLooser := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancelLooser()
		if got, _ := base.WithContext(looser).Deadline(); !got.Equal(time.Now().Add(time.Hour)) {
			t.Errorf("a later deadline does not loosen the call's: %v", got)
		}
		if got, ok := invocation.Background().WithContext(tighter).Deadline(); !ok || !got.Equal(time.Now().Add(time.Minute)) {
			t.Errorf("without a deadline of its own the context takes the new one: %v", got)
		}

		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if err := derived.Cancellation(); err == nil || err.Code != invocation.ErrorCodeDeadlineExceeded || !derived.IsExpired(time.Now()) {
			t.Errorf("the earlier deadline is attributed as a timeout: %v", err)
		}
		if base.Cancellation() != nil || base.DeadlineExceeded() {
			t.Error("the original context has not reached its deadline")
		}
	})
}
