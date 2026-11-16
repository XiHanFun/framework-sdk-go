package operation_test

import (
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
)

// assertReason 核对载荷不合法的 reason；want 为空表示解码成功
func assertReason(t *testing.T, invoker *operation.Invoker, id, payload, want string) {
	t.Helper()
	failure := invoker.Invoke(invocation.Background(), operation.Request{ID: id, Payload: []byte(payload)}).Outcome.Err()
	if want == "" {
		if failure != nil && failure.Code == operation.ErrorCodeInvalidPayload {
			t.Errorf("%q must decode, got %v", payload, failure)
		}
		return
	}
	if failure == nil || failure.Code != operation.ErrorCodeInvalidPayload || failure.Details["reason"] != want {
		t.Errorf("%q: %v, want reason %s", payload, failure, want)
	}
}

// TestPayloadsRunTheCommonJSONCheck 核对载荷先过共同的 JSON 读取检查（RFC-0001「JSON 文本」，RFC-0011 规则 2，ADR-0236）：
// 语法错误一律写 $，任何一层的重复成员写第一处重复的路径、先于其他问题；空载荷等价于 {}，只有空白不是 JSON
func TestPayloadsRunTheCommonJSONCheck(t *testing.T) {
	invoker := newInvoker(t, nil)
	cases := []struct{ payload, reason string }{
		{"", ""},
		{"   ", "$"},
		{"\xef\xbb\xbf" + `{"quantity":1}`, "$"},
		{`{"quantity":1,}`, "$"},
		{`{"quantity":1,"quantity":2}`, "$.quantity"},
		{`{"quantity":"x","other":{"k":1,"k":2}}`, "$.other.k"},
		{`{"quantity":1,"quantity":2,}`, "$"},
		{`{"quantity":1,"x":"\ud800"}`, "$"},
		{`{"quantity":1,"x":` + strings.Repeat("[", 64) + strings.Repeat("]", 64) + `}`, "$"},
		{`{"quantity":1,"x":` + strings.Repeat("[", 63) + strings.Repeat("]", 63) + `}`, ""},
	}
	for _, c := range cases {
		assertReason(t, invoker, "orders.create", c.payload, c.reason)
	}
}
