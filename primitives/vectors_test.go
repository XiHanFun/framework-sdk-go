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

// TestErrorsFollowTheWireEncodingVectors 回放 wire-encoding.json 的 errors
func TestErrorsFollowTheWireEncodingVectors(t *testing.T) {
	vectors, err := conformance.WireEncoding()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Errors.Valid {
		var decoded primitives.Error
		if err := json.Unmarshal(c.JSON, &decoded); err != nil {
			t.Errorf("%s: %s must be read: %v", c.Name, c.JSON, err)
			continue
		}
		encoded, err := json.Marshal(&decoded)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		assertSameJSON(t, c.Name, encoded, canonicalOf(c))
	}
	for _, c := range vectors.Errors.Invalid {
		var decoded primitives.Error
		if err := json.Unmarshal(c.JSON, &decoded); err == nil {
			t.Errorf("%s: %s must be rejected", c.Name, c.JSON)
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

// TestTemplatesFollowTheVectors 回放 message-templates.json 的 format：宽松与严格格式化的结果
func TestTemplatesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.MessageTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Format {
		arguments := argumentsOf(t, c.Arguments)
		if got := primitives.FormatMessage(c.Template, arguments); got != c.Lenient {
			t.Errorf("%s: FormatMessage(%q) = %q, want %q", c.Name, c.Template, got, c.Lenient)
		}
		strict, ok := primitives.TryFormatMessage(c.Template, arguments)
		switch {
		case c.Strict == nil && ok:
			t.Errorf("%s: TryFormatMessage(%q) must fail, got %q", c.Name, c.Template, strict)
		case c.Strict != nil && (!ok || strict != *c.Strict):
			t.Errorf("%s: TryFormatMessage(%q) = %q (%v), want %q", c.Name, c.Template, strict, ok, *c.Strict)
		}
		if placeholders, want := primitives.Placeholders(c.Template), rules.Placeholders(c.Template); !reflect.DeepEqual(placeholders, want) {
			t.Errorf("%s: Placeholders(%q) = %v, want %v", c.Name, c.Template, placeholders, want)
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

// TestBaggageValuesFollowTheVectors 回放 baggage-keys.json 的 values：已登记键的值能否进入调用上下文
func TestBaggageValuesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.BaggageKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range vectors.Values.Acceptable {
		if !primitives.IsBaggageValueAcceptable(value) || !primitives.IsBaggageEntryAcceptable(primitives.BaggageTenant, value) {
			t.Errorf("%q must be acceptable", value)
		}
	}
	for _, value := range vectors.Values.Unacceptable {
		if primitives.IsBaggageValueAcceptable(value) || primitives.IsBaggageEntryAcceptable(primitives.BaggageCorrelation, value) {
			t.Errorf("%q must be unacceptable", value)
		}
		if !primitives.IsBaggageEntryAcceptable(primitives.BaggageIdempotencyKey, value) || !primitives.IsBaggageEntryAcceptable("xihan.feature-flag", value) {
			t.Errorf("%q: the idempotency key and unregistered keys are not checked here", value)
		}
	}
}

// TestDuplicatesFollowTheWireEncodingVectors 回放 wire-encoding.json 的 duplicates：同一层有重复成员名的 Result 与 Error 必须被拒绝（ADR-0235）
func TestDuplicatesFollowTheWireEncodingVectors(t *testing.T) {
	vectors, err := conformance.WireEncoding()
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors.Duplicates) == 0 {
		t.Fatal("the vector has no duplicate cases")
	}
	for _, c := range vectors.Duplicates {
		var decodeErr error
		switch c.Target {
		case "result":
			_, decodeErr = roundTripResult(c.Kind, []byte(c.Text))
		case "error":
			var decoded primitives.Error
			decodeErr = json.Unmarshal([]byte(c.Text), &decoded)
		default:
			t.Fatalf("%s: unknown target %q", c.Name, c.Target)
		}
		if decodeErr == nil {
			t.Errorf("%s: %s must be rejected", c.Name, c.Text)
		}
		if rules.HasNoDuplicateMembers([]byte(c.Text)) == nil {
			t.Errorf("%s: the reference rule must see the duplicate too", c.Name)
		}
	}
}
