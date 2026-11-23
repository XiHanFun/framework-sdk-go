package primitives_test

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestBindMaybeChainsOptionalSteps 核对 BindMaybe：有值时把值交给返回 Maybe 的函数，无值原样传递、不调函数
func TestBindMaybeChainsOptionalSteps(t *testing.T) {
	calls := 0
	positive := func(text string) primitives.Maybe[int] {
		calls++
		if number, err := strconv.Atoi(text); err == nil && number > 0 {
			return primitives.Some(number)
		}
		return primitives.None[int]()
	}
	if bound := primitives.BindMaybe(primitives.Some("42"), positive); !bound.HasValue() || bound.Must() != 42 {
		t.Errorf("a value is handed to the function: %v", bound)
	}
	if bound := primitives.BindMaybe(primitives.Some("-1"), positive); bound.HasValue() {
		t.Errorf("the function may answer with no value: %v", bound)
	}
	if calls != 2 {
		t.Fatalf("the function was called %d times, want 2", calls)
	}
	if bound := primitives.BindMaybe(primitives.None[string](), positive); bound.HasValue() || calls != 2 {
		t.Errorf("no value is passed through without calling the function: %v after %d calls", bound, calls)
	}
	// 与 MapMaybe 配合：先变换、再接一步可能没有值的
	doubled := primitives.BindMaybe(primitives.MapMaybe(primitives.Some(21), func(n int) string { return strconv.Itoa(n * 2) }), positive)
	if value, ok := doubled.Value(); !ok || value != 42 {
		t.Errorf("chained with MapMaybe: %v", doubled)
	}
}

// TestMaybePrintsLikeTheReferenceImplementation 核对 Maybe.String：有值 Some(值)，无值 None，与 .NET 的 Maybe<T>.ToString 相同；
// fmt 的 %v 与 %s 都用它
func TestMaybePrintsLikeTheReferenceImplementation(t *testing.T) {
	cases := map[string]fmt.Stringer{
		"Some(42)":         primitives.Some(42),
		"Some(o1)":         primitives.Some("o1"),
		"Some()":           primitives.Some(""),
		"Some([1 2])":      primitives.Some([]int{1, 2}),
		"Some(Some(true))": primitives.Some(primitives.Some(true)),
		"None":             primitives.None[int](),
	}
	for want, maybe := range cases {
		if got := maybe.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
		if got := fmt.Sprintf("%v|%s", maybe, maybe); got != want+"|"+want {
			t.Errorf("fmt prints %q, want %q", got, want+"|"+want)
		}
	}
	var zero primitives.Maybe[string]
	if zero.String() != "None" {
		t.Errorf("the zero value has no value: %q", zero.String())
	}
}
