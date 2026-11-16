package invocation_test

import (
	"testing"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestBaggageValuesAreFilteredWhenTheContextIsBuilt 回放 baggage-keys.json 的 values：已登记键的值不合时，带 Baggage 创建或派生
// 上下文时丢弃这个成员；幂等键与未登记的键不在这里核对（ADR-0204、ADR-0233）
func TestBaggageValuesAreFilteredWhenTheContextIsBuilt(t *testing.T) {
	vectors, err := conformance.BaggageKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range vectors.Values.Acceptable {
		created := invocation.Background(invocation.WithBaggage(map[string]string{primitives.BaggageTenant: value}))
		if created.Tenant() != value {
			t.Errorf("%q must be kept when the context is created", value)
		}
		if derived := invocation.Background().WithBaggage(primitives.BaggagePrincipal, value); derived.Principal() != value {
			t.Errorf("%q must be kept when the context is derived", value)
		}
	}
	for _, value := range vectors.Values.Unacceptable {
		created := invocation.Background(invocation.WithBaggage(map[string]string{
			primitives.BaggageTenant:         value,
			primitives.BaggageIdempotencyKey: value,
			"xihan.feature-flag":             value,
		}))
		if _, ok := created.GetBaggage(primitives.BaggageTenant); ok {
			t.Errorf("%q must be dropped when the context is created", value)
		}
		if got, ok := created.GetBaggage(primitives.BaggageIdempotencyKey); !ok || got != value {
			t.Errorf("%q: the idempotency key is kept for the idempotency stage to reject", value)
		}
		if got, ok := created.GetBaggage("xihan.feature-flag"); !ok || got != value {
			t.Errorf("%q: unregistered keys are left to the carriers", value)
		}
		kept := invocation.Background(
			invocation.WithBaggage(map[string]string{primitives.BaggageCorrelation: "c1"}),
			invocation.WithBaggage(map[string]string{primitives.BaggageCorrelation: value}))
		if kept.Correlation() != "c1" {
			t.Errorf("%q: external input keeps the earlier value, got %q", value, kept.Correlation())
		}
		base := invocation.Background(invocation.WithBaggage(map[string]string{primitives.BaggageCorrelation: "c1"}))
		assertFrameworkPanic(t, value, func() { base.WithBaggage(primitives.BaggageCorrelation, value) })
		if derived := base.WithBaggage(primitives.BaggageIdempotencyKey, value); derived.IdempotencyKey() != value {
			t.Errorf("%q: deriving the idempotency key is not checked", value)
		}
		if derived := base.WithBaggage("xihan.feature-flag", value); func() string { v, _ := derived.GetBaggage("xihan.feature-flag"); return v }() != value {
			t.Errorf("%q: deriving an unregistered key is not checked", value)
		}
	}
}

// assertFrameworkPanic 核对 fn 以 PRIMITIVES_VALIDATION_001 的 FrameworkError panic：程序内逐项派生给已登记的键一个不合的值是编程错误（ADR-0235）
func assertFrameworkPanic(t *testing.T, value string, fn func()) {
	t.Helper()
	defer func() {
		framework, ok := recover().(*primitives.FrameworkError)
		if !ok || framework.Code != primitives.ErrorCodeInvalidArgument {
			t.Errorf("%q: deriving a registered key with it must panic with %s", value, primitives.ErrorCodeInvalidArgument)
		}
	}()
	fn()
}
