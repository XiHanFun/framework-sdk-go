package jsoncodec

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// profile 与 inner 照 payload-reasons.json 的请求类型 profile 声明：name 必填，inner.n 必填，其余可选
type inner struct {
	N int64 `json:"n" xihan:"required"`
}

type profile struct {
	Name   string   `json:"name" xihan:"required"`
	Note   string   `json:"note"`
	Count  int64    `json:"count"`
	Ratio  float64  `json:"ratio"`
	Flag   bool     `json:"flag"`
	Tags   []string `json:"tags"`
	Scores []int64  `json:"scores"`
	Inner  *inner   `json:"inner"`
	Meta   any      `json:"meta"`
}

// problemOf 解码并返回第一处问题的路径；解码成功为空
func problemOf[T any](t *testing.T, text string) (T, string) {
	t.Helper()
	var target T
	root, problem := jsontext.Parse([]byte(text))
	if problem != nil {
		return target, problem.Path
	}
	if problem := Decode([]byte(text), root, &target); problem != nil {
		return target, problem.Path
	}
	return target, ""
}

// TestDecodeFollowsThePayloadRules 核对 RFC-0011 规则 2 的解码语义与报错顺序
func TestDecodeFollowsThePayloadRules(t *testing.T) {
	cases := []struct{ text, path string }{
		{`{}`, "$"},
		{`{"name":"a"}`, ""},
		{`{"name":null}`, ""},
		{`{"name":5}`, "$.name"},
		{`{"name":"a","note":null}`, ""},
		{`{"name":"a","count":null}`, "$.count"},
		{`{"name":"a","count":1.0}`, "$.count"},
		{`{"name":"a","count":1e2}`, "$.count"},
		{`{"name":"a","count":9223372036854775807}`, ""},
		{`{"name":"a","count":9223372036854775808}`, "$.count"},
		{`{"name":"a","count":-9223372036854775808}`, ""},
		{`{"name":"a","count":-0}`, ""},
		{`{"name":"a","count":"5"}`, "$.count"},
		{`{"name":"a","ratio":5}`, ""},
		{`{"name":"a","ratio":1e400}`, ""},
		{`{"name":"a","ratio":1e-400}`, ""},
		{`{"name":"a","ratio":null}`, "$.ratio"},
		{`{"name":"a","ratio":"1.5"}`, "$.ratio"},
		{`{"name":"a","flag":1}`, "$.flag"},
		{`{"name":"a","flag":null}`, "$.flag"},
		{`{"name":"a","tags":null}`, ""},
		{`{"name":"a","tags":["x",null]}`, ""},
		{`{"name":"a","tags":["x",1]}`, "$.tags[1]"},
		{`{"name":"a","tags":"x"}`, "$.tags"},
		{`{"name":"a","scores":[1,null]}`, "$.scores[1]"},
		{`{"name":"a","scores":[1,2.5]}`, "$.scores[1]"},
		{`{"name":"a","inner":null}`, ""},
		{`{"name":"a","inner":{}}`, "$.inner"},
		{`{"name":"a","inner":{"n":"x"}}`, "$.inner.n"},
		{`{"name":"a","inner":[]}`, "$.inner"},
		{`{"name":"a","meta":{"x":[1,{"y":null}]}}`, ""},
		{`{"name":"a","meta":null}`, ""},
		{`{"Name":"a"}`, "$"},
		{`{"name":"a","extra":{"deep":[1,2]}}`, ""},
		{`{"count":"x"}`, "$.count"},
		{`{"inner":{},"count":"x"}`, "$.inner"},
		{`{"count":1}`, "$"},
		{`null`, ""},
		{`[]`, "$"},
	}
	for _, c := range cases {
		if _, path := problemOf[profile](t, c.text); path != c.path {
			t.Errorf("%s: problem at %q, want %q", c.text, path, c.path)
		}
	}
	decoded, _ := problemOf[profile](t, `{"name":"a","ratio":1e400,"meta":{"n":9007199254740993,"f":2.0},"count":-0}`)
	if !math.IsInf(decoded.Ratio, 1) || decoded.Count != 0 {
		t.Errorf("decoded %+v", decoded)
	}
	if encoded, err := Marshal(decoded.Meta); err != nil || string(encoded) != `{"f":2.0,"n":9007199254740993}` {
		t.Errorf("any JSON writes back what it read: %s (%v)", encoded, err)
	}
	if meta := decoded.Meta.(map[string]any); meta["n"] != json.Number("9007199254740993") || meta["f"] != json.Number("2.0") {
		t.Errorf("meta %#v", meta)
	}
}

// TestDecodeRangesAndTypes 核对整数的范围、无符号、映射的键、自定义解码与 json:",string"
func TestDecodeRangesAndTypes(t *testing.T) {
	if _, path := problemOf[map[string]int32](t, `{"a":2147483648}`); path != "$.a" {
		t.Errorf("int32 overflow at %q", path)
	}
	if _, path := problemOf[map[string]int32](t, `{"a":-2147483648}`); path != "" {
		t.Errorf("int32 minimum at %q", path)
	}
	if _, path := problemOf[map[string]uint8](t, `{"a":-1}`); path != "$.a" {
		t.Errorf("negative uint at %q", path)
	}
	if value, path := problemOf[map[string]float32](t, `{"a":1e39}`); path != "" || !math.IsInf(float64(value["a"]), 1) {
		t.Errorf("float32 overflow %v at %q", value, path)
	}
	if value, path := problemOf[map[int]string](t, `{"1":"x","2":"y"}`); path != "" || value[2] != "y" {
		t.Errorf("int keys %v at %q", value, path)
	}
	if _, path := problemOf[map[int]string](t, `{"x":"y"}`); path != "$.x" {
		t.Errorf("bad int key at %q", path)
	}
	if value, path := problemOf[struct {
		When time.Time `json:"when"`
	}](t, `{"when":"2026-11-16T00:00:00Z"}`); path != "" || value.When.Year() != 2026 {
		t.Errorf("time %v at %q", value, path)
	}
	if _, path := problemOf[struct {
		When time.Time `json:"when"`
	}](t, `{"when":"yesterday"}`); path != "$.when" {
		t.Errorf("bad time at %q", path)
	}
	if value, path := problemOf[struct {
		Raw json.RawMessage `json:"raw"`
	}](t, `{"raw":{"a":[1, 2]}}`); path != "" || string(value.Raw) != `{"a":[1, 2]}` {
		t.Errorf("raw %s at %q", value.Raw, path)
	}
	if value, path := problemOf[struct {
		Count int `json:"count,string"`
	}](t, `{"count":"12"}`); path != "" || value.Count != 12 {
		t.Errorf("quoted %v at %q", value, path)
	}
	if value, path := problemOf[struct{ Data []byte }](t, `{"Data":"aGk="}`); path != "" || string(value.Data) != "hi" {
		t.Errorf("bytes %v at %q", value, path)
	}
	if value, path := problemOf[struct{ Data []byte }](t, `{"data":"aGk="}`); path != "" || value.Data != nil {
		t.Errorf("member names are case-sensitive: %v at %q", value, path)
	}
	// 落在 json.Number 上的字符串必须整个是一个 JSON 数字：带首尾空白的不算
	type numbered struct {
		N json.Number `json:"n"`
	}
	if value, path := problemOf[numbered](t, `{"n":"12.50"}`); path != "" || value.N != "12.50" {
		t.Errorf("a number in a string: %v at %q", value, path)
	}
	for _, text := range []string{`" 1"`, `"1 "`, `"\t1"`, `"+1"`, `"01"`, `""`} {
		if _, path := problemOf[numbered](t, `{"n":`+text+`}`); path != "$.n" {
			t.Errorf("%s is not a JSON number, got the problem at %q", text, path)
		}
	}
}

type base struct {
	ID   string `json:"id" xihan:"required"`
	Kind string `json:"kind"`
}

type derived struct {
	base
	Kind  int `json:"kind"`
	Extra string
}

// TestEmbeddedFieldsArePromoted 核对嵌入结构体的成员照 encoding/json 提升，浅层的同名成员胜出，必填成员一并核对
func TestEmbeddedFieldsArePromoted(t *testing.T) {
	value, path := problemOf[derived](t, `{"id":"a","kind":2,"Extra":"x"}`)
	if path != "" || value.ID != "a" || value.Kind != 2 || value.Extra != "x" {
		t.Errorf("%+v at %q", value, path)
	}
	if _, path := problemOf[derived](t, `{"kind":2}`); path != "$" {
		t.Errorf("missing embedded required member at %q", path)
	}
	encoded, err := Marshal(derived{base: base{ID: "a"}, Kind: 2})
	if err != nil || string(encoded) != `{"id":"a","kind":2,"Extra":""}` {
		t.Errorf("encoded %s (%v)", encoded, err)
	}
}

// TestValueModelRoundTrips 核对值模型（ADR-0236）：64 位范围内的整数原样往返，整数值的浮点仍是浮点，非有限值写成字符串
func TestValueModelRoundTrips(t *testing.T) {
	root, _ := jsontext.Parse([]byte(`{"n":9007199254740993,"m":-9223372036854775808,"big":9223372036854775808,"f":2.0,"z":-0.0,"zi":-0,"e":1e17,"inf":1e400}`))
	value := FromJSON(root).(map[string]any)
	want := map[string]any{"n": int64(9007199254740993), "m": int64(math.MinInt64), "big": 9223372036854775808.0, "f": 2.0, "z": math.Copysign(0, -1), "zi": int64(0), "e": 1e17, "inf": math.Inf(1)}
	for name, expected := range want {
		if got := value[name]; reflect.TypeOf(got) != reflect.TypeOf(expected) || got != expected || (name == "z" && !math.Signbit(got.(float64))) {
			t.Errorf("%s: %#v, want %#v", name, got, expected)
		}
	}
	cases := []struct {
		value any
		want  string
	}{
		{[]any{2.0, math.Copysign(0, -1), int64(2), 1e17, 1e16, 0.1, float32(0.1)}, `[2.0,-0.0,2,1E+17,10000000000000000.0,0.1,0.10000000149011612]`},
		{[]any{math.NaN(), math.Inf(1), math.Inf(-1)}, `["NaN","Infinity","-Infinity"]`},
		{uint64(math.MaxUint64), `1.8446744073709552E+19`},
		{map[string]any{"b": "q\"/é<", "a": nil}, `{"a":null,"b":"q\"/é<"}`},
		{json.Number("12.50"), `12.50`},
		{struct {
			A float64 `json:"a,omitempty"`
			B []int   `json:"b"`
			C *int    `json:"c,omitempty"`
		}{B: []int{}}, `{"b":[]}`},
	}
	for _, c := range cases {
		encoded, err := Marshal(c.value)
		if err != nil || string(encoded) != c.want {
			t.Errorf("%#v: %s (%v), want %s", c.value, encoded, err, c.want)
		}
	}
	if _, err := Marshal(map[string]any{"f": func() {}}); err == nil {
		t.Error("a function cannot be written")
	}
}

// TestMarshalRejectsInvalidNumbers 核对 json.Number 写出前要整个是一个 JSON 数字（与 encoding/json 相同，空串写 0）：
// 不是数字的文本原样写出会让整份 JSON 不合法，甚至夹带别的成员，所以报错、什么都不写
func TestMarshalRejectsInvalidNumbers(t *testing.T) {
	for text, want := range map[string]string{"": "0", "0": "0", "-12": "-12", "12.50": "12.50", "1e400": "1e400", "9007199254740993": "9007199254740993"} {
		if encoded, err := Marshal(json.Number(text)); err != nil || string(encoded) != want {
			t.Errorf("json.Number(%q): %s (%v), want %s", text, encoded, err, want)
		}
	}
	for _, text := range []string{" 1", "1 ", "\t1", "+1", "01", "1.", "1e", "abc", "NaN", "Infinity", `"1"`, `1,"admin":true`, "1}"} {
		number := json.Number(text)
		values := map[string]any{
			"a bare number":      number,
			"a member":           map[string]any{"n": number},
			"an item":            []any{1, number},
			"a field":            struct{ N json.Number }{number},
			"a pointer":          &number,
			"a typed collection": []json.Number{"1", number},
		}
		for name, value := range values {
			if encoded, err := Marshal(value); err == nil {
				t.Errorf("%s holding json.Number(%q) must not be written, got %s", name, text, encoded)
			}
		}
	}
}
