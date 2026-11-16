package invocation_test

import (
	"context"
	"strings"
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

// TestTraceFlagsFollowRFC0003 核对 RFC-0003 规则 7：没有上游时标志为空（根调用），写出时写 01；上游的标志原样保留，
// 采样只看最低位；只给 ID 不给标志按已采样处理
func TestTraceFlagsFollowRFC0003(t *testing.T) {
	root := invocation.Background()
	if _, ok := root.TraceFlags(); ok {
		t.Error("a root invocation has no trace flags")
	}
	if !strings.HasSuffix(root.TraceParent(), "-01") {
		t.Errorf("without a sampler the root writes 01: %s", root.TraceParent())
	}
	ignored := invocation.Background(invocation.WithTraceParent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331"))
	if _, ok := ignored.TraceFlags(); ok || ignored.TraceID() == "0af7651916cd43dd8448eb211c80319c" {
		t.Error("an invalid traceparent is ignored and the invocation is a root")
	}

	const prefix = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-"
	for flags, sampled := range map[string]bool{"00": false, "01": true, "02": false, "03": true, "ff": true, "fe": false} {
		ctx := invocation.Background(invocation.WithTraceParent(prefix + flags))
		got, ok := ctx.TraceFlags()
		written := prefix + "00"
		if sampled {
			written = prefix + "01"
		}
		if !ok || ctx.TraceParent() != written || invocation.IsSampled(got) != sampled || got > invocation.SampledFlag {
			t.Errorf("flags %s: %02x %v, traceparent %s", flags, got, ok, ctx.TraceParent())
		}
		if _, _, parsed, ok := invocation.ParseTraceParent(prefix + flags); !ok || parsed != sampled {
			t.Errorf("flags %s parse as sampled=%v", flags, parsed)
		}
	}

	explicit := invocation.Background(invocation.WithTrace("0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"))
	if flags, ok := explicit.TraceFlags(); !ok || flags != invocation.SampledFlag || explicit.TraceParent() != prefix+"01" {
		t.Errorf("ids without flags count as sampled: %02x %v %s", flags, ok, explicit.TraceParent())
	}
	if derived := invocation.Background(invocation.WithTraceParent(prefix+"00")).WithBaggage("xihan.feature-flag", "on"); derived.TraceParent() != prefix+"00" {
		t.Errorf("derived contexts keep the flags: %s", derived.TraceParent())
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

// TestTraceParentGrammarIsVersion00Only 核对 RFC-0003 规则 7 的文法：只认版本 00 与小写十六进制，不合法的当作没给（ADR-0235）
func TestTraceParentGrammarIsVersion00Only(t *testing.T) {
	for _, bad := range []string{
		"01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-0A",
		"00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01",
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
		" 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	} {
		if _, _, _, ok := invocation.ParseTraceParent(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
		if ctx := invocation.Background(invocation.WithTraceParent(bad)); ctx.TraceID() == "0af7651916cd43dd8448eb211c80319c" {
			t.Errorf("%q must be ignored", bad)
		}
	}
}
