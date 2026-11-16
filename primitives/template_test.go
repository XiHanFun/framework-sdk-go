package primitives_test

import (
	"encoding/json"
	"math"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestTemplateSyntax 核对 RFC-0010 的模板文法：按名字或按位置的占位符、{{ 与 }} 转义、不合法的花括号原样当字面量
func TestTemplateSyntax(t *testing.T) {
	arguments := map[string]any{"name": "x", "0": "zero", "a_1": 1, "Upper": "u"}
	cases := map[string]string{
		"{name}":                      "x",
		"{0} and {name}":              "zero and x",
		"{a_1}{Upper}":                "1u",
		"{{name}}":                    "{name}",
		"{{{name}}}":                  "{x}",
		"}}":                          "}",
		"a } b":                       "a } b",
		"a { b":                       "a { b",
		"a {name":                     "a {name",
		"a { {{ b {name}":             "a { {{ b {name}",
		"{_x}":                        "{_x}",
		"{1a}":                        "{1a}",
		"{a b}":                       "{a b}",
		"{a-b}":                       "{a-b}",
		"{}":                          "{}",
		"{:D2}":                       "{:D2}",
		`{"json": 1}`:                 `{"json": 1}`,
		"{a{name}":                    "{a{name}",
		"{name}}":                     "x}",
		"{missing} and {missing:D3}":  "{missing} and {missing:D3}",
		"{missing:} {name:}":          "{missing:} x",
		"{name:yyyy-MM-dd} {name:N2}": "x x",
		"中文 {name} 文本":                "中文 x 文本",
		"":                            "",
		"plain":                       "plain",
	}
	for template, want := range cases {
		if got := primitives.FormatMessage(template, arguments); got != want {
			t.Errorf("FormatMessage(%q) = %q, want %q", template, got, want)
		}
	}
	if got := primitives.FormatMessage("{0}/{1}", primitives.Positional("a", 2)); got != "a/2" {
		t.Errorf("positional = %q", got)
	}
	if got := primitives.Placeholders("{{x}} {a} {b:D2} {a} {_c} {0} {b} {"); !reflect.DeepEqual(got, []string{"a", "b", "0"}) {
		t.Errorf("placeholders = %v", got)
	}
}

// TestTryFormatMessageIsStrict 核对严格格式化：任一占位符缺值即失败
func TestTryFormatMessageIsStrict(t *testing.T) {
	if text, ok := primitives.TryFormatMessage("{a} {b:D2} {{c}}", map[string]any{"a": 1, "b": 2}); !ok || text != "1 02 {c}" {
		t.Errorf("complete arguments: %q %v", text, ok)
	}
	if _, ok := primitives.TryFormatMessage("{a} {b}", map[string]any{"a": 1}); ok {
		t.Error("a missing argument must fail")
	}
	if text, ok := primitives.TryFormatMessage("no placeholders {_x}", nil); !ok || text != "no placeholders {_x}" {
		t.Errorf("literal braces are not placeholders: %q %v", text, ok)
	}
}

// TestPortableFormatSpecifiers 核对可移植的格式说明符子集：D、X / x、F、N，F 与 N 按精确值舍入、正好一半时取偶
func TestPortableFormatSpecifiers(t *testing.T) {
	cases := []struct {
		value  any
		format string
		want   string
	}{
		{7, "D3", "007"},
		{-7, "D3", "-007"},
		{1234, "D2", "1234"},
		{7, "D", "7"},
		{7, "D0", "7"},
		{int64(math.MinInt64), "D", "-9223372036854775808"},
		{uint64(math.MaxUint64), "D25", "0000018446744073709551615"},
		{2.0, "D3", "002"},
		{json.Number("12"), "D4", "0012"},
		{2.5, "D3", "2.5"},
		{"7", "D3", "7"},
		{true, "D3", "true"},
		{255, "X", "FF"},
		{255, "x4", "00ff"},
		{uint8(10), "X2", "0A"},
		{0, "X", "0"},
		{-1, "X", "-1"},
		{0.125, "F2", "0.12"},
		{0.375, "F2", "0.38"},
		{2.675, "F2", "2.67"},
		{2.5, "F0", "2"},
		{-2.5, "F0", "-2"},
		{3.5, "F0", "4"},
		{1.005, "F2", "1.00"},
		{7, "F", "7.00"},
		{-7, "F1", "-7.0"},
		{0.5, "F", "0.50"},
		{1.0 / 3, "F4", "0.3333"},
		{float32(0.1), "F9", "0.100000001"},
		{json.Number("2.675"), "F2", "2.68"},
		{json.Number("2.665"), "F2", "2.66"},
		{-0.001, "F2", "-0.00"},
		{1234567.891, "N", "1,234,567.89"},
		{-1234.5, "N0", "-1,234"},
		{1235.5, "N0", "1,236"},
		{999.999, "N2", "1,000.00"},
		{12, "N1", "12.0"},
		{123, "N0", "123"},
		{1000, "N0", "1,000"},
		{-123456, "N", "-123,456.00"},
		{math.NaN(), "F2", "NaN"},
		{math.Inf(1), "N", "Infinity"},
		{42, "yyyy-MM-dd", "42"},
		{0.5, "P2", "0.5"},
		{7, "d3", "007"},
		{2.25, "f1", "2.2"},
		{1234, "n0", "1,234"},
		{255, "X4", "00FF"},
		{-42, "D5", "-00042"},
		{0.3333333333333333, "F12", "0.333333333333"},
		{1, "F99", "1." + strings.Repeat("0", 99)},
		{7, "D3x", "7"},
		{7, "D1000", "7"},
		{7, "", "7"},
		{nil, "D3", ""},
	}
	for _, c := range cases {
		if got := primitives.FormatArgument(c.value, c.format); got != c.want {
			t.Errorf("FormatArgument(%#v, %q) = %q, want %q", c.value, c.format, got, c.want)
		}
	}
	if got := primitives.FormatMessage("{count:D3} items cost {total:N2}", map[string]any{"count": 7, "total": 1234.5}); got != "007 items cost 1,234.50" {
		t.Errorf("formatted message = %q", got)
	}
}

// TestCatalogResolvesStrictlyAndIgnoresLocaleCase 核对消息目录：区域名不区分大小写，缺键回退 en-US，缺参数即解析失败
func TestCatalogResolvesStrictlyAndIgnoresLocaleCase(t *testing.T) {
	catalog := primitives.Catalog{
		"en-US": {"orders.missing": "Order {id} was not found.", "orders.only_english": "English {id}."},
		"zh-CN": {"orders.missing": "订单 {id} 不存在。"},
	}
	arguments := map[string]any{"id": 42}
	for _, locale := range []string{"zh-CN", "zh-cn", "ZH-CN"} {
		if text, ok := catalog.Resolve("orders.missing", locale, arguments); !ok || text != "订单 42 不存在。" {
			t.Errorf("%s: %q %v", locale, text, ok)
		}
	}
	if text, ok := catalog.Resolve("orders.only_english", "zh-cn", arguments); !ok || text != "English 42." {
		t.Errorf("fallback to en-US: %q %v", text, ok)
	}
	if text, ok := catalog.Resolve("orders.missing", "EN-us", arguments); !ok || text != "Order 42 was not found." {
		t.Errorf("en-US in another case: %q %v", text, ok)
	}
	if _, ok := catalog.Resolve("orders.missing", "zh-CN", nil); ok {
		t.Error("a missing argument must fail the resolution")
	}
	if _, ok := catalog.Resolve("orders.unknown", "zh-CN", arguments); ok {
		t.Error("an unknown key must fail the resolution")
	}

	primitives.SetDefaultMessages(catalog, "zh-cn")
	t.Cleanup(func() { primitives.SetDefaultMessages(nil, "en-US") })
	if got := primitives.NotFound("ORDERS_NOTFOUND_001", "orders.missing", arguments).Message(); got != "订单 42 不存在。" {
		t.Errorf("message = %q", got)
	}
	if got := primitives.NotFound("ORDERS_NOTFOUND_001", "orders.missing", map[string]any{"other": 1}).Message(); got != "orders.missing (other=1)" {
		t.Errorf("a template that cannot be filled falls back to Describe: %q", got)
	}
}

// TestFixedFloatsRoundLikeStrconv 核对 F 与 N 写浮点时与 strconv.FormatFloat(x, 'f', n, 64) 一致：按二进制值的精确展开舍入、
// 正好一半取偶，与 .NET 10 的实测一致；负零写 -0.00
func TestFixedFloatsRoundLikeStrconv(t *testing.T) {
	measured := []struct {
		value  float64
		format string
		want   string
	}{
		{0.125, "F2", "0.12"}, {0.375, "F2", "0.38"}, {2.5, "F0", "2"}, {-2.5, "F0", "-2"}, {3.5, "F0", "4"},
		{1234.5, "N0", "1,234"}, {2.675, "F2", "2.67"}, {math.Copysign(0, -1), "F2", "-0.00"}, {math.Copysign(0, -1), "N1", "-0.0"},
	}
	for _, c := range measured {
		if got := primitives.FormatArgument(c.value, c.format); got != c.want {
			t.Errorf("FormatArgument(%v, %q) = %q, want %q", c.value, c.format, got, c.want)
		}
	}
	random := rand.New(rand.NewSource(1))
	for index := 0; index < 20000; index++ {
		value := (random.Float64() - 0.5) * math.Pow(10, float64(random.Intn(16)-4))
		if index%4 == 0 {
			value = math.Round(value*8) / 8 // 正好一半的二进制值
		}
		precision := random.Intn(8)
		if got, want := primitives.FormatArgument(value, "F"+strconv.Itoa(precision)), strconv.FormatFloat(value, 'f', precision, 64); got != want {
			t.Fatalf("FormatArgument(%v, F%d) = %q, strconv %q", value, precision, got, want)
		}
	}
}
