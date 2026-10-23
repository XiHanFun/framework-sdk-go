package invocation_test

import (
	"context"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

func TestNewGeneratesTraceAndKeepsBaggage(t *testing.T) {
	ctx := invocation.Background(invocation.WithBaggage(map[string]string{primitives.BaggageTenant: "t1"}))
	if len(ctx.TraceID()) != 32 || len(ctx.SpanID()) != 16 {
		t.Errorf("trace %s span %s", ctx.TraceID(), ctx.SpanID())
	}
	if ctx.Tenant() != "t1" || ctx.Principal() != "" {
		t.Error("baggage accessors")
	}
	derived := ctx.WithBaggage(primitives.BaggageCorrelation, "c1").WithBaggage("xihan.feature-flag", "on")
	if derived.Correlation() != "c1" || ctx.Correlation() != "" {
		t.Error("derived baggage must not leak back")
	}
	if _, ok := derived.WithoutUnregisteredBaggage().GetBaggage("xihan.feature-flag"); ok {
		t.Error("unregistered keys are stripped")
	}
	derived.Set("key", 1)
	if value, ok := ctx.Get("key"); !ok || value != 1 {
		t.Error("items are shared between derived contexts")
	}
	if traceID, spanID, sampled, ok := invocation.ParseTraceParent(ctx.TraceParent()); !ok || traceID != ctx.TraceID() || spanID != ctx.SpanID() || !sampled {
		t.Errorf("traceparent %s", ctx.TraceParent())
	}
}

func TestDeadlineCancelsAndTranslates(t *testing.T) {
	ctx := invocation.Background(invocation.WithTimeout(20 * time.Millisecond))
	if !ctx.HasDeadline() {
		t.Fatal("deadline")
	}
	if remaining, ok := ctx.RemainingTime(time.Now()); !ok || remaining > 20*time.Millisecond {
		t.Errorf("remaining %v", remaining)
	}
	<-ctx.Done()
	if err := ctx.Cancellation(); err == nil || err.Code != invocation.ErrorCodeDeadlineExceeded {
		t.Errorf("err %v", err)
	}
	if ctx.Err() == nil {
		t.Error("the embedded context error is still available")
	}
	if !ctx.IsExpired(time.Now()) {
		t.Error("expired")
	}

	parent, cancel := context.WithCancel(context.Background())
	cancelled := invocation.New(parent)
	cancel()
	<-cancelled.Done()
	if err := cancelled.Cancellation(); err == nil || err.Code != invocation.ErrorCodeCancelled {
		t.Errorf("err %v", err)
	}
	if invocation.Background().Cancellation() != nil {
		t.Error("no error without cancellation")
	}
}

func TestTraceParentParsing(t *testing.T) {
	if _, _, _, ok := invocation.ParseTraceParent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"); !ok {
		t.Error("valid traceparent")
	}
	for _, bad := range []string{"", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331", "00-00000000000000000000000000000000-b7ad6b7169203331-01", "00-0AF7651916CD43DD8448EB211C80319C-b7ad6b7169203331-01"} {
		if _, _, _, ok := invocation.ParseTraceParent(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
	ctx := invocation.Background(invocation.WithTraceParent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"))
	if ctx.TraceID() != "0af7651916cd43dd8448eb211c80319c" || ctx.TraceParent() != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00" {
		t.Errorf("traceparent %s", ctx.TraceParent())
	}
}

func TestMalformedBaggageKeysPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a malformed key must panic")
		}
	}()
	invocation.Background().WithBaggage("tenant", "t1")
}
