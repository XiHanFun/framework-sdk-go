package operation_test

import (
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/primitives"
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

// payloadInner 与 payloadProfile 照 payload-reasons.json 的请求类型 profile：必填成员写 xihan:"required"
type payloadInner struct {
	N int64 `json:"n" xihan:"required"`
}

type payloadProfile struct {
	Name   string        `json:"name" xihan:"required"`
	Note   string        `json:"note"`
	Count  int64         `json:"count"`
	Ratio  float64       `json:"ratio"`
	Flag   bool          `json:"flag"`
	Tags   []string      `json:"tags"`
	Scores []int64       `json:"scores"`
	Inner  *payloadInner `json:"inner"`
	Meta   any           `json:"meta"`
}

// profileInvoker 登记 profiles.post 与 counts.post 两个回声操作
func profileInvoker(t *testing.T) *operation.Invoker {
	t.Helper()
	registry := operation.NewRegistry()
	if err := operation.Add(registry, operation.Contract{ID: "profiles.post"}, operation.HandlerFunc[payloadProfile, payloadProfile](func(_ *invocation.Context, request payloadProfile) primitives.Result[payloadProfile] {
		return primitives.Ok(request)
	}), nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := operation.Add(registry, operation.Contract{ID: "counts.post"}, operation.HandlerFunc[map[string]int32, map[string]int32](func(_ *invocation.Context, request map[string]int32) primitives.Result[map[string]int32] {
		return primitives.Ok(request)
	}), nil, ""); err != nil {
		t.Fatal(err)
	}
	invoker, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return invoker
}

// TestPayloadsDecodeByTheRequestType 核对载荷的解码语义（RFC-0011 规则 2，ADR-0236）：成员名区分大小写、未知成员忽略、数字不从字符串读、
// 整数不接受小数点与指数、超出范围即不符、浮点上溢为无穷；整数、浮点、布尔不接受 null；必填成员缺少时在所在对象结束时报；按文本顺序报第一处
func TestPayloadsDecodeByTheRequestType(t *testing.T) {
	invoker := profileInvoker(t)
	cases := []struct{ id, payload, reason string }{
		{"profiles.post", ``, "$"},
		{"profiles.post", `{}`, "$"},
		{"profiles.post", `{"name":null}`, ""},
		{"profiles.post", `{"Name":"a"}`, "$"},
		{"profiles.post", `{"name":"a","count":1.0}`, "$.count"},
		{"profiles.post", `{"name":"a","count":9223372036854775808}`, "$.count"},
		{"profiles.post", `{"name":"a","count":"5"}`, "$.count"},
		{"profiles.post", `{"name":"a","ratio":1e400}`, ""},
		{"profiles.post", `{"name":"a","flag":null}`, "$.flag"},
		{"profiles.post", `{"name":"a","tags":["x",null]}`, ""},
		{"profiles.post", `{"name":"a","scores":[1,null]}`, "$.scores[1]"},
		{"profiles.post", `{"name":"a","inner":{}}`, "$.inner"},
		{"profiles.post", `{"count":"x"}`, "$.count"},
		{"profiles.post", `{"inner":{},"count":"x"}`, "$.inner"},
		{"profiles.post", `{"count":1}`, "$"},
		{"profiles.post", `{"count":"x","name":"a","name":"b"}`, "$.name"},
		{"profiles.post", `{"name":"a","n\u0061me":"b"}`, "$.name"},
		{"counts.post", `{"a":2147483648}`, "$.a"},
		{"counts.post", `{"a":-2147483648}`, ""},
		{"counts.post", `{"a\u0085b":"x"}`, "$['a\u0085b']"},
		{"counts.post", `42`, "$"},
	}
	for _, c := range cases {
		assertReason(t, invoker, c.id, c.payload, c.reason)
	}
	response := invoker.Invoke(invocation.Background(), operation.Request{ID: "profiles.post", Payload: []byte(`{"name":"a","meta":{"n":9007199254740993,"f":2.0}}`)})
	if !response.Outcome.OK() || string(response.Payload) != `{"name":"a","note":"","count":0,"ratio":0,"flag":false,"tags":null,"scores":null,"inner":null,"meta":{"f":2,"n":9007199254740993}}` {
		t.Errorf("response %v %s", response.Outcome, response.Payload)
	}
}
