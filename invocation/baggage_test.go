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
		base := invocation.Background(invocation.WithBaggage(map[string]string{primitives.BaggageCorrelation: "c1"}))
		if derived := base.WithBaggage(primitives.BaggageCorrelation, value); derived.Correlation() != "c1" {
			t.Errorf("%q must be dropped when the context is derived, keeping the previous value, got %q", value, derived.Correlation())
		}
	}
}
