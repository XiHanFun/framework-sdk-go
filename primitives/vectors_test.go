package primitives_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xihanfun/framework-spec/conformance"
	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestResultsFollowTheWireEncodingVectors 回放 wire-encoding.json 的 results：合法样例读得出、再写出等于规范形式，非法样例被拒绝
func TestResultsFollowTheWireEncodingVectors(t *testing.T) {
	vectors, err := conformance.WireEncoding()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Results.Valid {
		encoded, err := roundTripResult(c.Kind, c.JSON)
		if err != nil {
			t.Errorf("%s: %s must be read: %v", c.Name, c.JSON, err)
			continue
		}
		assertSameJSON(t, c.Name, encoded, canonicalOf(c))
	}
	for _, c := range vectors.Results.Invalid {
		if encoded, err := roundTripResult(c.Kind, c.JSON); err == nil {
			t.Errorf("%s: %s must be rejected, read as %s", c.Name, c.JSON, encoded)
		}
	}
}

// roundTripResult 按样例的种类读结果再写出：value 是 Result[any]，unit 是 Result[Unit]，maybe 是 Result[Maybe[any]]
func roundTripResult(kind string, data []byte) ([]byte, error) {
	switch kind {
	case rules.ResultValue:
		var result primitives.Result[any]
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		return json.Marshal(result)
	case rules.ResultUnit:
		var result primitives.Result[primitives.Unit]
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		return json.Marshal(result)
	case rules.ResultMaybe:
		var result primitives.Result[primitives.Maybe[any]]
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	panic("unknown result kind " + kind)
}

// canonicalOf 返回样例再写出时应得的 JSON：没有 canonical 时就是原样
func canonicalOf(c conformance.WireCase) []byte {
	if len(c.Canonical) > 0 {
		return c.Canonical
	}
	return c.JSON
}

// assertSameJSON 按 JSON 值比较，不比较成员顺序与空白
func assertSameJSON(t *testing.T, name string, got, want []byte) {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		t.Errorf("%s: written %s is not JSON: %v", name, got, err)
		return
	}
	if err := json.Unmarshal(want, &right); err != nil {
		t.Fatalf("%s: vector %s is not JSON: %v", name, want, err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Errorf("%s: written %s, want %s", name, got, want)
	}
}

// TestMessageKeysFollowTheTemplateVectors 回放 message-templates.json 的 keys
func TestMessageKeysFollowTheTemplateVectors(t *testing.T) {
	vectors, err := conformance.MessageTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range vectors.Keys.Valid {
		if !primitives.IsMessageKey(key) {
			t.Errorf("%q must be valid", key)
		}
	}
	for _, key := range vectors.Keys.Invalid {
		if primitives.IsMessageKey(key) {
			t.Errorf("%q must be invalid", key)
		}
	}
}

// TestDescribeFollowsTheVectors 回放 message-templates.json 的 describe
func TestDescribeFollowsTheVectors(t *testing.T) {
	vectors, err := conformance.MessageTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Describe {
		if got := primitives.Describe(c.Key, argumentsOf(t, c.Arguments)); got != c.Text {
			t.Errorf("Describe(%q, %s) = %q, want %q", c.Key, c.Arguments, got, c.Text)
		}
	}
}

// argumentsOf 按向量的约定把参数交给格式化：null 是没有参数；JSON 数字带小数点或指数的是浮点，否则是整数
func argumentsOf(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var arguments map[string]any
	if err := decoder.Decode(&arguments); err != nil {
		t.Fatalf("arguments %s: %v", raw, err)
	}
	for name, value := range arguments {
		number, ok := value.(json.Number)
		if !ok {
			continue
		}
		if strings.ContainsAny(number.String(), ".eE") {
			arguments[name], _ = number.Float64()
		} else {
			arguments[name], _ = number.Int64()
		}
	}
	return arguments
}
