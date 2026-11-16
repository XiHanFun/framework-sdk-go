package primitives_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestResultEncodesValuesAndUnitSuccess 核对 RFC-0001 的写出：有值的成功带 value，没有值的成功不写 value，失败只带 error
func TestResultEncodesValuesAndUnitSuccess(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"value", primitives.Ok(map[string]any{"id": "o1"}), `{"ok":true,"value":{"id":"o1"}}`},
		{"zero value", primitives.Ok(0), `{"ok":true,"value":0}`},
		{"unit", primitives.Success(), `{"ok":true}`},
		{"maybe with a value", primitives.Ok(primitives.Some("x")), `{"ok":true,"value":"x"}`},
		{"maybe without a value", primitives.Ok(primitives.None[string]()), `{"ok":true,"value":null}`},
		{"failure", primitives.Fail[int](primitives.Literal("ORDERS_CONFLICT_001", "stale", nil, nil)), `{"ok":false,"error":{"code":"ORDERS_CONFLICT_001","message":"stale"}}`},
		{"unit failure", primitives.Fail[primitives.Unit](primitives.Literal("ORDERS_CONFLICT_001", "stale", nil, nil)), `{"ok":false,"error":{"code":"ORDERS_CONFLICT_001","message":"stale"}}`},
	}
	for _, c := range cases {
		data, err := json.Marshal(c.value)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(data) != c.want {
			t.Errorf("%s: encoded %s, want %s", c.name, data, c.want)
		}
	}
	if data, err := json.Marshal(primitives.Ok[*int](nil)); err == nil {
		t.Errorf("a null value that is not a Maybe must not be written, got %s", data)
	}
}

// TestResultDecodingFollowsTheWireRules 核对读取：ok 必须是布尔值，成功与失败各自的成员必填或禁止，成员名区分大小写，未知成员忽略
func TestResultDecodingFollowsTheWireRules(t *testing.T) {
	var number primitives.Result[int]
	if err := json.Unmarshal([]byte(`{"ok":true,"value":5,"extra":[1]}`), &number); err != nil || number.Or(0) != 5 {
		t.Errorf("value: %v %v", number, err)
	}
	var unit primitives.Result[primitives.Unit]
	if err := json.Unmarshal([]byte(`{"ok":true}`), &unit); err != nil || !unit.OK() {
		t.Errorf("unit: %v %v", unit, err)
	}
	var maybe primitives.Result[primitives.Maybe[int]]
	if err := json.Unmarshal([]byte(`{"ok":true,"value":null}`), &maybe); err != nil || !maybe.OK() || maybe.Must().HasValue() {
		t.Errorf("maybe: %v %v", maybe, err)
	}
	var failed primitives.Result[int]
	if err := json.Unmarshal([]byte(`{"ok":false,"error":{"code":"ORDERS_CONFLICT_001","message":"stale"},"extra":1}`), &failed); err != nil || failed.OK() || failed.Err().Code != "ORDERS_CONFLICT_001" {
		t.Errorf("failure: %v %v", failed, err)
	}

	rejected := map[string]func([]byte) error{
		`null`:                      decodeInto[int],
		`[]`:                        decodeInto[int],
		`"ok"`:                      decodeInto[int],
		`{"value":1}`:               decodeInto[int],
		`{"OK":true,"value":1}`:     decodeInto[int],
		`{"ok":"true","value":1}`:   decodeInto[int],
		`{"ok":1,"value":1}`:        decodeInto[int],
		`{"ok":null,"value":1}`:     decodeInto[int],
		`{"ok":true}`:               decodeInto[int],
		`{"ok":true,"Value":1}`:     decodeInto[int],
		`{"ok":true,"value":null}`:  decodeInto[int],
		`{"ok":true,"value":"one"}`: decodeInto[int],
		`{"ok":true,"value":{}}`:    decodeInto[primitives.Unit],
		`{"ok":true,"value":null} `: decodeInto[primitives.Unit],
		`{"ok":true,"value":1,"error":{"code":"ORDERS_CONFLICT_001","message":"stale"}}`: decodeInto[int],
		`{"ok":true,"error":null}`:                                           decodeInto[primitives.Unit],
		`{"ok":false}`:                                                       decodeInto[int],
		`{"ok":false,"error":null}`:                                          decodeInto[int],
		`{"ok":false,"error":[]}`:                                            decodeInto[int],
		`{"ok":false,"Error":{"code":"A_B_001","message":"m"}}`:              decodeInto[int],
		`{"ok":false,"error":{"code":"A_B_001","message":"m"},"value":1}`:    decodeInto[int],
		`{"ok":false,"error":{"code":"A_B_001","message":"m"},"value":null}`: decodeInto[primitives.Unit],
	}
	for data, decode := range rejected {
		err := decode([]byte(data))
		if err == nil {
			t.Errorf("%s must be rejected", data)
			continue
		}
		var framework *primitives.FrameworkError
		if !errors.As(err, &framework) || framework.Code != primitives.ErrorCodeInvalidArgument {
			t.Errorf("%s: rejected with %v, want %s", data, err, primitives.ErrorCodeInvalidArgument)
		}
	}
}

// decodeInto 把 data 读成 Result[T]
func decodeInto[T any](data []byte) error {
	var result primitives.Result[T]
	return json.Unmarshal(data, &result)
}

// TestDuplicateMembersAreRejected 核对 Result 与 Error 同一层的重复成员名不合法，不取第一个也不取最后一个（ADR-0235）
func TestDuplicateMembersAreRejected(t *testing.T) {
	for _, data := range []string{
		`{"ok":true,"ok":false}`,
		`{"ok":true,"value":1,"value":2}`,
		`{"ok":false,"error":{"code":"KERNEL_UNEXPECTED_001","message":"a"},"error":{"code":"KERNEL_UNEXPECTED_001","message":"b"}}`,
		`{"ok":false,"error":{"code":"KERNEL_UNEXPECTED_001","code":"KERNEL_NOTFOUND_001","message":"x"}}`,
		`{"ok":true,"value":{"id":1,"id":2}}`,
	} {
		if err := decodeInto[any]([]byte(data)); err == nil {
			t.Errorf("%s must be rejected", data)
		}
	}
	var failure primitives.Error
	if err := json.Unmarshal([]byte(`{"code":"KERNEL_UNEXPECTED_001","message":"x","inner":{"code":"KERNEL_UNEXPECTED_001","message":"a","message":"b"}}`), &failure); err == nil {
		t.Error("a duplicate member in the inner error must be rejected")
	}
	if err := json.Unmarshal([]byte(`{"code":"KERNEL_UNEXPECTED_001","message":"x","details":{"a":1,"a":2}}`), &failure); err == nil {
		t.Error("a duplicate member in the details must be rejected")
	}
}

// TestWireReadingRunsTheCommonJSONCheck 核对 Result 与 Error 先过共同的 JSON 读取检查（RFC-0001「JSON 文本」，ADR-0236）：
// BOM、嵌套超过 64 层、孤立的代理项与非法 UTF-8 都不是 JSON，任何一层的重复成员都不合法
func TestWireReadingRunsTheCommonJSONCheck(t *testing.T) {
	deep := strings.Repeat("[", 64) + strings.Repeat("]", 64)
	for _, data := range []string{
		"\xef\xbb\xbf" + `{"ok":true}`,
		`{"ok":true,"value":` + deep + `}`,
		`{"ok":true,"value":"\ud800"}`,
		`{"ok":true,"value":"` + "\xff" + `"}`,
		`{"ok":true,"value":[{"a":1},{"b":1,"b":2}]}`,
		`{"ok":true,"value":1,}`,
	} {
		var result primitives.Result[any]
		if err := result.UnmarshalJSON([]byte(data)); err == nil {
			t.Errorf("%q must be rejected", data)
		}
	}
	if err := decodeInto[any]([]byte(`{"ok":true,"value":` + strings.Repeat("[", 63) + strings.Repeat("]", 63) + `}`)); err != nil {
		t.Errorf("64 levels are allowed: %v", err)
	}
	for _, data := range []string{
		`{"code":"KERNEL_UNEXPECTED_001","message":"x","details":{"a":"\udc00"}}`,
		`{"code":"KERNEL_UNEXPECTED_001","message":"x","inner":{"code":"KERNEL_UNEXPECTED_001","message":"y","details":{"n":1,"n":2}}}`,
		`{"code":"KERNEL_UNEXPECTED_001","message":"x","details":{"a":{"b":1,"b":2}}}`,
	} {
		var failure primitives.Error
		if err := failure.UnmarshalJSON([]byte(data)); err == nil {
			t.Errorf("%q must be rejected", data)
		}
	}
}

// TestWireNumbersFollowTheValueModel 核对线上的数字按值模型往返（RFC-0001「Result」，ADR-0236）：64 位范围内的整数精确往返，
// 整数值的浮点写出时仍是浮点（2.0、-0.0），非有限值写成字符串
func TestWireNumbersFollowTheValueModel(t *testing.T) {
	cases := map[string]string{
		`{"ok":true,"value":{"m":-9223372036854775808,"n":9007199254740993}}`: `{"ok":true,"value":{"m":-9223372036854775808,"n":9007199254740993}}`,
		`{"ok":true,"value":[2.0,-0.0,2,1e17,0.5]}`:                           `{"ok":true,"value":[2.0,-0.0,2,1E+17,0.5]}`,
	}
	for data, want := range cases {
		var result primitives.Result[any]
		if err := json.Unmarshal([]byte(data), &result); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		encoded, err := json.Marshal(result)
		if err != nil || string(encoded) != want {
			t.Errorf("%s: %s (%v), want %s", data, encoded, err, want)
		}
	}
	var typed primitives.Result[map[string]any]
	if err := json.Unmarshal([]byte(`{"ok":true,"value":{"n":9007199254740993}}`), &typed); err != nil || typed.Must()["n"] != int64(9007199254740993) {
		t.Errorf("typed value %v (%v)", typed, err)
	}
	failure := primitives.Literal("KERNEL_UNEXPECTED_001", "x", map[string]any{"f": 2.0, "z": math.Copysign(0, -1), "e": 1e17, "n": 2, "nan": math.NaN(), "big": uint64(math.MaxUint64)}, nil)
	encoded, err := json.Marshal(failure)
	if want := `{"code":"KERNEL_UNEXPECTED_001","message":"x","details":{"big":1.8446744073709552E+19,"e":1E+17,"f":2.0,"n":2,"nan":"NaN","z":-0.0}}`; err != nil || string(encoded) != want {
		t.Errorf("details %s (%v), want %s", encoded, err, want)
	}
	var decoded primitives.Error
	if err := json.Unmarshal([]byte(`{"code":"KERNEL_UNEXPECTED_001","message":"x","details":{"n":9007199254740993,"f":2.0}}`), &decoded); err != nil || decoded.Details["n"] != int64(9007199254740993) || decoded.Details["f"] != 2.0 {
		t.Errorf("decoded details %#v (%v)", decoded.Details, err)
	}
	var strict primitives.Result[struct {
		Count int `json:"count"`
	}]
	if err := json.Unmarshal([]byte(`{"ok":true,"value":{"Count":"x"}}`), &strict); err != nil || strict.Must().Count != 0 {
		t.Errorf("member names of a typed value are case-sensitive: %v (%v)", strict, err)
	}
	if err := json.Unmarshal([]byte(`{"ok":true,"value":{"count":1.5}}`), &strict); err == nil {
		t.Error("1.5 is not an integer")
	}
}
