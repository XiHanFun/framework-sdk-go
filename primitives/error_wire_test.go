package primitives_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestErrorEncodesMembersInOrder 核对写出：code、message、messageKey、details、inner 的顺序，message 总有，其余缺省不写
func TestErrorEncodesMembersInOrder(t *testing.T) {
	inner := primitives.Literal("KERNEL_UNEXPECTED_001", "boom", nil, nil)
	cases := []struct {
		name string
		err  *primitives.Error
		want string
	}{
		{"literal", inner, `{"code":"KERNEL_UNEXPECTED_001","message":"boom"}`},
		{"literal with details", primitives.Literal("ORDERS_CONFLICT_001", "stale", map[string]any{"id": "o1"}, nil), `{"code":"ORDERS_CONFLICT_001","message":"stale","details":{"id":"o1"}}`},
		{"keyed without details", primitives.Validation("ORDERS_VALIDATION_001", "orders.quantity.invalid", map[string]any{}), `{"code":"ORDERS_VALIDATION_001","message":"orders.quantity.invalid","messageKey":"orders.quantity.invalid"}`},
		{"everything", primitives.NewError("ORDERS_UNEXPECTED_001", "orders.failed", map[string]any{"id": "o1"}, inner), `{"code":"ORDERS_UNEXPECTED_001","message":"orders.failed (id=o1)","messageKey":"orders.failed","details":{"id":"o1"},"inner":{"code":"KERNEL_UNEXPECTED_001","message":"boom"}}`},
	}
	for _, c := range cases {
		data, err := json.Marshal(c.err)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(data) != c.want {
			t.Errorf("%s: encoded %s, want %s", c.name, data, c.want)
		}
	}
}

// TestErrorDecodingFollowsTheWireRules 核对读取：code 必填且合法，messageKey 有就必须合法，details 与 inner 必须是对象，未知成员忽略
func TestErrorDecodingFollowsTheWireRules(t *testing.T) {
	var decoded primitives.Error
	data := `{"code":"ORDERS_UNEXPECTED_001","message":"failed","messageKey":"orders.failed","details":{"id":"o1"},"inner":{"code":"KERNEL_UNEXPECTED_001","message":"boom","extra":true},"extra":[1]}`
	if err := json.Unmarshal([]byte(data), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Code != "ORDERS_UNEXPECTED_001" || decoded.Text != "failed" || decoded.MessageKey != "orders.failed" || decoded.Details["id"] != "o1" || decoded.Inner == nil || decoded.Inner.Code != "KERNEL_UNEXPECTED_001" || decoded.Inner.Text != "boom" {
		t.Errorf("decoded %+v", decoded)
	}
	for _, data := range []string{
		`null`, `[]`, `"ORDERS_NOTFOUND_001"`,
		`{}`, `{"Code":"ORDERS_NOTFOUND_001","message":"m"}`, `{"code":null,"message":"m"}`, `{"code":1,"message":"m"}`, `{"code":"orders","message":"m"}`,
		`{"code":"ORDERS_NOTFOUND_001","message":null}`, `{"code":"ORDERS_NOTFOUND_001","message":1}`,
		`{"code":"ORDERS_NOTFOUND_001","messageKey":"orders.missing"}`, `{"code":"ORDERS_NOTFOUND_001","message":""}`, `{"code":"ORDERS_NOTFOUND_001","Message":"m"}`,
		`{"code":"ORDERS_NOTFOUND_001","message":"m","messageKey":""}`, `{"code":"ORDERS_NOTFOUND_001","message":"m","messageKey":null}`,
		`{"code":"ORDERS_NOTFOUND_001","message":"m","messageKey":"Orders.Missing"}`, `{"code":"ORDERS_NOTFOUND_001","message":"m","messageKey":"orders"}`,
		`{"code":"ORDERS_NOTFOUND_001","message":"m","details":null}`, `{"code":"ORDERS_NOTFOUND_001","message":"m","details":[]}`, `{"code":"ORDERS_NOTFOUND_001","message":"m","details":"id"}`,
		`{"code":"ORDERS_NOTFOUND_001","message":"m","inner":null}`, `{"code":"ORDERS_NOTFOUND_001","message":"m","inner":[]}`, `{"code":"ORDERS_NOTFOUND_001","message":"m","inner":{"message":"no code"}}`,
	} {
		var target primitives.Error
		err := json.Unmarshal([]byte(data), &target)
		var framework *primitives.FrameworkError
		if !errors.As(err, &framework) || framework.Code != primitives.ErrorCodeInvalidArgument {
			t.Errorf("%s must be rejected with %s, got %v", data, primitives.ErrorCodeInvalidArgument, err)
		}
	}
}

// nestedArrays 套 depth 层数组，最里面是 0
func nestedArrays(depth int) any {
	var value any = 0
	for range depth {
		value = []any{value}
	}
	return value
}

// assertNotWritten 核对 MarshalJSON 写不出：返回 PRIMITIVES_VALIDATION_001 的 FrameworkError、不交出文本；经 json.Marshal 也一样
func assertNotWritten(t *testing.T, name string, value json.Marshaler) {
	t.Helper()
	encoded, err := value.MarshalJSON()
	var framework *primitives.FrameworkError
	if !errors.As(err, &framework) || framework.Code != primitives.ErrorCodeInvalidArgument || encoded != nil {
		t.Errorf("%s: must not be written, got %q (%v)", name, encoded, err)
	}
	if encoded, err := json.Marshal(value); err == nil || !errors.As(err, &framework) {
		t.Errorf("%s: json.Marshal must fail the same way, got %q (%v)", name, encoded, err)
	}
}

// TestErrorsThatAreNotJSONTextAreNotWritten 核对 RFC-0001「写出与转交」（ADR-0320）：Error 的线上编码里的字符串（文案、细节的成员名与值，
// 含内部错误的）不是合法的 UTF-8（含编成 UTF-8 的孤立代理项）即写不出，不写成 U+FFFD 或 \ud800 这样的转义；写出的整段文本还要过
// 共同检查，嵌套从错误的根算起超过 64 层、细节里原样嵌进去的文本有重复成员或孤立代理项的转义同样写不出；多字节文本、真正的 U+FFFD
// 与正好 64 层照常写出
func TestErrorsThatAreNotJSONTextAreNotWritten(t *testing.T) {
	literal := func(text string, details map[string]any) *primitives.Error {
		return primitives.Literal("ORDERS_CONFLICT_001", text, details, nil)
	}
	unwritable := map[string]*primitives.Error{
		"a surrogate in the message":        literal("a\xed\xa0\x80", nil),
		"an invalid byte in the message":    literal("a\xffb", nil),
		"a surrogate in a detail":           literal("stale", map[string]any{"id": "o\xed\xb0\x80"}),
		"an invalid byte in a detail":       literal("stale", map[string]any{"id": []any{"ok", "\xff"}}),
		"a surrogate in a detail name":      literal("stale", map[string]any{"i\xed\xa0\x80d": 1}),
		"an invalid byte in a nested name":  literal("stale", map[string]any{"a": map[string]any{"\xff": 1}}),
		"a raw duplicate in the details":    literal("stale", map[string]any{"raw": json.RawMessage(`{"a":1,"a":2}`)}),
		"a raw surrogate escape":            literal("stale", map[string]any{"raw": json.RawMessage(`"\ud800"`)}),
		"details 65 levels from the root":   literal("stale", map[string]any{"deep": nestedArrays(63)}),
		"a keyed error rendering bad bytes": primitives.NewError("ORDERS_CONFLICT_001", "orders.conflict", map[string]any{"id": "\xff"}, nil),
		"an inner error that is not text":   primitives.Literal("ORDERS_CONFLICT_001", "stale", nil, literal("inner", map[string]any{"id": "\xff"})),
	}
	for name, failure := range unwritable {
		assertNotWritten(t, name, failure)
	}
	writable := map[string]struct {
		err  *primitives.Error
		want string
	}{
		"multibyte and a real U+FFFD": {literal("汉字\U0001F600", map[string]any{"r": "\xef\xbf\xbd"}), "{\"code\":\"ORDERS_CONFLICT_001\",\"message\":\"汉字\U0001F600\",\"details\":{\"r\":\"\xef\xbf\xbd\"}}"},
		"64 levels from the root":     {literal("stale", map[string]any{"deep": nestedArrays(62)}), `{"code":"ORDERS_CONFLICT_001","message":"stale","details":{"deep":` + strings.Repeat("[", 62) + "0" + strings.Repeat("]", 62) + `}}`},
	}
	for name, c := range writable {
		if encoded, err := c.err.MarshalJSON(); err != nil || string(encoded) != c.want {
			t.Errorf("%s: got %q (%v), want %q", name, encoded, err, c.want)
		}
	}
}
