package primitives_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestNewFrameworkErrorFormatsTheMessage 核对 NewFrameworkError：码原样，消息按 fmt.Sprintf 组装，没有底层错误
func TestNewFrameworkErrorFormatsTheMessage(t *testing.T) {
	failure := primitives.NewFrameworkError(primitives.ErrorCodeInvalidArgument, "The operation ID '%s' must match %s.", "Orders", "<resource>.<verb>")
	if failure.Code != primitives.ErrorCodeInvalidArgument || failure.Message != "The operation ID 'Orders' must match <resource>.<verb>." || failure.Cause != nil {
		t.Errorf("framework error %+v", failure)
	}
	if got := failure.Error(); got != "PRIMITIVES_VALIDATION_001: The operation ID 'Orders' must match <resource>.<verb>." {
		t.Errorf("Error() = %q", got)
	}
	if plain := primitives.NewFrameworkError("ORDERS_VALIDATION_009", "Misconfigured."); plain.Code != "ORDERS_VALIDATION_009" || plain.Message != "Misconfigured." {
		t.Errorf("without arguments: %+v", plain)
	}
	var found *primitives.FrameworkError
	if wrapped := fmt.Errorf("serve: %w", failure); !errors.As(wrapped, &found) || found != failure {
		t.Errorf("errors.As must find the framework error, got %v", found)
	}
	if online := failure.ToError(); online.Code != primitives.ErrorCodeInvalidArgument || online.Message() != failure.Message {
		t.Errorf("ToError %v", online)
	}
}

// TestErrorUnwrapsToItsInner 核对 Error.Unwrap：返回内部错误，errors.Is 与 errors.As 沿着 Inner 走；没有内部错误时返回的是 nil 接口，
// 不是装着 nil 指针的 error
func TestErrorUnwrapsToItsInner(t *testing.T) {
	inner := primitives.NotFound("ORDERS_NOTFOUND_001", "orders.missing", map[string]any{"id": "o1"})
	outer := primitives.Unexpected("ORDERS_UNEXPECTED_001", "orders.failed", nil, inner)
	if unwrapped := errors.Unwrap(outer); unwrapped != error(inner) {
		t.Errorf("Unwrap returns the inner error, got %v", unwrapped)
	}
	if unwrapped := inner.Unwrap(); unwrapped != nil {
		t.Errorf("without an inner error Unwrap must return a nil interface, got %#v", unwrapped)
	}
	if errors.Unwrap(inner) != nil {
		t.Error("errors.Unwrap of an error without an inner error is nil")
	}
	// 按错误码比较（Error.Is）沿着链走到内部错误
	if !errors.Is(outer, primitives.NotFound("ORDERS_NOTFOUND_001", "orders.other", nil)) {
		t.Error("errors.Is must match the code of the inner error")
	}
	if errors.Is(outer, primitives.Conflict("ORDERS_CONFLICT_001", "orders.conflict", nil)) {
		t.Error("errors.Is must not match a code that is not in the chain")
	}
	// 包了一层的错误里 errors.As 找到最外层的 Error
	var found *primitives.Error
	if wrapped := fmt.Errorf("store: %w", outer); !errors.As(wrapped, &found) || found != outer {
		t.Errorf("errors.As must find the outer error, got %v", found)
	}
}
