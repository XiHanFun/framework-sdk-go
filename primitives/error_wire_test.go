package primitives_test

import (
	"encoding/json"
	"errors"
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
	var bare primitives.Error
	if err := json.Unmarshal([]byte(`{"code":"ORDERS_NOTFOUND_001","messageKey":"orders.missing"}`), &bare); err != nil || bare.Message() != "orders.missing" {
		t.Errorf("without a message the key describes it: %+v %v", bare, err)
	}

	for _, data := range []string{
		`null`, `[]`, `"ORDERS_NOTFOUND_001"`,
		`{}`, `{"Code":"ORDERS_NOTFOUND_001","message":"m"}`, `{"code":null,"message":"m"}`, `{"code":1,"message":"m"}`, `{"code":"orders","message":"m"}`,
		`{"code":"ORDERS_NOTFOUND_001","message":null}`, `{"code":"ORDERS_NOTFOUND_001","message":1}`,
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
