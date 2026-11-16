package primitives_test

import (
	"encoding/json"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestArraysAndObjectsAreCompactJSON 核对数组与对象写成紧凑的 JSON（RFC-0010「模板」，ADR-0236）：对象成员按名字的 UTF-16 码元序，
// 嵌套的 null 写 null，数字写法与顶层相同，字符串只转义必须转义的字符，超过 64 层的容器写 null
func TestArraysAndObjectsAreCompactJSON(t *testing.T) {
	deep := any([]any{})
	for range 64 {
		deep = []any{deep}
	}
	cases := []struct {
		value any
		want  string
	}{
		{[]any{1, "a", true, nil, 1.5}, `[1,"a",true,null,1.5]`},
		{map[string]any{"b": 2, "a": []any{1, map[string]any{"d": nil, "c": "x"}}}, `{"a":[1,{"c":"x","d":null}],"b":2}`},
		{map[string]int{"ｚ": 1, "\U0001F600": 2, "b": 3, "B": 4, "é": 5}, "{\"B\":4,\"b\":3,\"é\":5,\"\U0001F600\":2,\"ｚ\":1}"},
		{[]string{"q\"b\\s/\n\t\x01\x1f\x7f é"}, `["q\"b\\s/\n\t\u0001\u001f` + "\x7f é\"]"},
		{[]any{1e17, math.Copysign(0, -1), 2.0, 0.1, math.NaN()}, `[1E+17,-0,2,0.1,NaN]`},
		{[]int{}, `[]`},
		{map[string]any{}, `{}`},
		{map[int]string{2: "b", 10: "a"}, `{"10":"a","2":"b"}`},
		{[]byte{1, 2}, `[1,2]`},
		{[]any(nil), ``},
		{json.RawMessage(`{"b":[1.5,2],"a":9007199254740993}`), `{"a":9007199254740993,"b":[1.5,2]}`},
		{[]*int{nil}, `[null]`},
		{[]time.Duration{time.Second}, `["1s"]`},
		{big.NewInt(42), `42`},
		{new(big.Int).Lsh(big.NewInt(1), 70), `1.1805916207174113E+21`},
		{strings.Repeat("x", 3), `xxx`},
	}
	for _, c := range cases {
		if got := primitives.FormatValue(c.value); got != c.want {
			t.Errorf("FormatValue(%#v) = %s, want %s", c.value, got, c.want)
		}
	}
	if got := primitives.FormatValue(deep); got != strings.Repeat("[", 64)+"null"+strings.Repeat("]", 64) {
		t.Errorf("a container deeper than 64 levels is null: %s", got)
	}
	if got := primitives.FormatArgument([]any{1.5}, "F2"); got != "[1.5]" {
		t.Errorf("arrays ignore the specifier: %s", got)
	}
	if got := primitives.Describe("orders.line.invalid", map[string]any{"v": []any{1, "x"}, "m": map[string]any{"b": 1, "a": nil}}); got != `orders.line.invalid (m={"a":null,"b":1}, v=[1,"x"])` {
		t.Errorf("describe %s", got)
	}
}

// TestOnlyPortableSpecifiersAreRecognised 核对说明符只认 ^[DdFfNnXx][0-9]{0,2}$，不适用时按无格式写出（ADR-0236）
func TestOnlyPortableSpecifiersAreRecognised(t *testing.T) {
	cases := []struct {
		value  any
		format string
		want   string
	}{
		{5, "D100", "5"},
		{0.1, "F100", "0.1"},
		{0.1, "F99", "0.1" + "00000000000000005551115123125782702118158340454101562500000000000000000000000000000000000000000000"},
		{5, "F02", "5.00"},
		{1234.5, "E2", "1234.5"},
		{1234.5, "G", "1234.5"},
		{0.5, "P", "0.5"},
		{5, "0.00", "5"},
		{1234567, "#,##0", "1234567"},
		{5, "yyyy", "5"},
		{5, " F2", "5"},
		{5, "F2 ", "5"},
		{5, "", "5"},
		{5, "D-1", "5"},
		{5, "FF", "5"},
		{true, "F2", "true"},
		{"str", "F2", "str"},
		{nil, "D2", ""},
		{2.5, "D3", "2.5"},
		{-1, "X", "-1"},
		{2.5, "x", "2.5"},
		{5, "F2", "5.00"},
		{-1234567, "N2", "-1,234,567.00"},
		{1e20, "N0", "100,000,000,000,000,000,000"},
		{math.Inf(1), "F2", "Infinity"},
		{math.NaN(), "N", "NaN"},
		{int64(math.MinInt64), "D", "-9223372036854775808"},
		{int64(math.MinInt64), "N0", "-9,223,372,036,854,775,808"},
	}
	for _, c := range cases {
		if got := primitives.FormatArgument(c.value, c.format); got != c.want {
			t.Errorf("FormatArgument(%#v, %q) = %q, want %q", c.value, c.format, got, c.want)
		}
	}
}

// TestLocalesOnlyChangeTheSeparators 核对区域只改变小数点与分组符（RFC-0010「模板」的固定表，ADR-0236）
func TestLocalesOnlyChangeTheSeparators(t *testing.T) {
	cases := []struct {
		locale, template string
		value            any
		want             string
	}{
		{"de-DE", "{v}", -1234.5, "-1234,5"},
		{"de-DE", "{v:N2}", -1234.5, "-1.234,50"},
		{"de-DE", "{v:F1}", -1234.5, "-1234,5"},
		{"de-DE", "{v}", 1234567, "1234567"},
		{"de-DE", "{v:D5}", 42, "00042"},
		{"de-DE", "{v:x}", 255, "ff"},
		{"de-DE", "{v}", 1.5e20, "1,5E+20"},
		{"de-DE", "{v}", []any{1.5}, "[1.5]"},
		{"de-DE", "{v}", math.Inf(-1), "-Infinity"},
		{"de-de", "{v:N2}", 1234.5, "1.234,50"},
		{"es-ES", "{v:N2}", 1234567.5, "1.234.567,50"},
		{"pt-BR", "{v:N2}", 1234567.5, "1.234.567,50"},
		{"fr-FR", "{v:N2}", 1234567.5, "1" + string(rune(0x202F)) + "234" + string(rune(0x202F)) + "567,50"},
		{"ru-RU", "{v:N2}", 1234567.5, "1" + string(rune(0xA0)) + "234" + string(rune(0xA0)) + "567,50"},
		{"en-US", "{v:N2} {v}", 1234567.5, "1,234,567.50 1234567.5"},
		{"zh-CN", "{v:N2}", 1234567.5, "1,234,567.50"},
		{"it-IT", "{v:N2} {v}", 1234567.5, "1,234,567.50 1234567.5"},
		{"", "{v:N2}", 1234567.5, "1,234,567.50"},
	}
	for _, c := range cases {
		arguments := map[string]any{"v": c.value}
		if got := primitives.FormatMessageIn(c.locale, c.template, arguments); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.locale, c.template, got, c.want)
		}
		if got, ok := primitives.TryFormatMessageIn(c.locale, c.template, arguments); !ok || got != c.want {
			t.Errorf("strict %s %s: %q, want %q", c.locale, c.template, got, c.want)
		}
	}
	if got := primitives.FormatArgumentIn("de-DE", 1234.5, "N1"); got != "1.234,5" {
		t.Errorf("FormatArgumentIn %s", got)
	}
	catalog := primitives.Catalog{
		"de-DE": {"orders.total": "Summe {v:N2}"},
		"en-US": {"orders.total": "Total {v:N2}", "orders.fallback": "Fallback {v:N2}"},
	}
	if text, ok := catalog.Resolve("orders.total", "DE-de", map[string]any{"v": 1234.5}); !ok || text != "Summe 1.234,50" {
		t.Errorf("catalog %q", text)
	}
	if text, ok := catalog.Resolve("orders.fallback", "de-DE", map[string]any{"v": 1234.5}); !ok || text != "Fallback 1,234.50" {
		t.Errorf("a template from the fallback locale formats with its own symbols: %q", text)
	}
}
