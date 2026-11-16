package invocation_test

import (
	"testing"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/invocation"
)

// TestTraceContextFollowsTheVectors 回放 trace-context.json：合法的 traceparent 解析成给出的标识与采样位，不合法的当作没有给出；
// 写出时版本 00、标志只写采样位，没有标志时写 01（RFC-0003 规则 7，ADR-0235）
func TestTraceContextFollowsTheVectors(t *testing.T) {
	vectors, err := conformance.TraceContext()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Parse.Valid {
		traceID, spanID, sampled, ok := invocation.ParseTraceParent(c.Text)
		if !ok || traceID != c.TraceID || spanID != c.SpanID || sampled != c.Sampled {
			t.Errorf("%q parsed as %s %s %v (ok %v)", c.Text, traceID, spanID, sampled, ok)
		}
		ctx := invocation.Background(invocation.WithTraceParent(c.Text))
		flags, hasFlags := ctx.TraceFlags()
		if ctx.TraceID() != c.TraceID || ctx.SpanID() != c.SpanID || !hasFlags || invocation.IsSampled(flags) != c.Sampled {
			t.Errorf("%q: context %s %s flags %02x %v", c.Text, ctx.TraceID(), ctx.SpanID(), flags, hasFlags)
		}
	}
	for _, text := range vectors.Parse.Invalid {
		if _, _, _, ok := invocation.ParseTraceParent(text); ok {
			t.Errorf("%q must not parse", text)
		}
		ctx := invocation.Background(invocation.WithTraceParent(text))
		if _, hasFlags := ctx.TraceFlags(); hasFlags {
			t.Errorf("%q must be treated as absent", text)
		}
	}
	for _, c := range vectors.Format {
		var ctx *invocation.Context
		switch {
		case c.Sampled == nil:
			ctx = invocation.Background(invocation.WithTrace(c.TraceID, c.SpanID))
		case *c.Sampled:
			ctx = invocation.Background(invocation.WithTraceParent("00-" + c.TraceID + "-" + c.SpanID + "-01"))
		default:
			ctx = invocation.Background(invocation.WithTraceParent("00-" + c.TraceID + "-" + c.SpanID + "-00"))
		}
		if got := ctx.TraceParent(); got != c.Text {
			t.Errorf("format %s/%s/%v = %q, want %q", c.TraceID, c.SpanID, c.Sampled, got, c.Text)
		}
	}
	root := invocation.Background()
	if root.TraceParent() != "00-"+root.TraceID()+"-"+root.SpanID()+"-01" {
		t.Errorf("a root invocation without a sampler writes 01: %s", root.TraceParent())
	}
}
