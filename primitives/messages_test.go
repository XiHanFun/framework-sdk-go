package primitives_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

type quantity int

type label string

// TestFormatValueWritesPortableText 核对模板与 Describe 共用的值文本（RFC-0010）
func TestFormatValueWritesPortableText(t *testing.T) {
	var nilError *primitives.Error
	cases := []struct {
		value any
		want  string
	}{
		{"x", "x"},
		{"", ""},
		{42, "42"},
		{-7, "-7"},
		{int64(math.MinInt64), "-9223372036854775808"},
		{uint64(math.MaxUint64), "18446744073709551615"},
		{true, "true"},
		{false, "false"},
		{nil, ""},
		{nilError, ""},
		{2.0, "2"},
		{-2.0, "-2"},
		{0.1, "0.1"},
		{1.5, "1.5"},
		{0.0001, "0.0001"},
		{0.00015, "0.00015"},
		{123456789012345.6, "123456789012345.6"},
		{999999999999999.0, "999999999999999"},
		{1e16, "10000000000000000"},
		{9.99e16, "99900000000000000"},
		{1e17, "1E+17"},
		{1.2345678901234568e17, "1.2345678901234568E+17"},
		{-1e17, "-1E+17"},
		{1e100, "1E+100"},
		{0.00012, "0.00012"},
		{0.000099, "9.9E-05"},
		{1.5e-7, "1.5E-07"},
		{5e-324, "5E-324"},
		{0.0, "0"},
		{math.Copysign(0, -1), "-0"},
		{math.NaN(), "NaN"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{0.30000000000000004, "0.30000000000000004"},
		{float32(0.1), "0.1"},
		{float32(2.5), "2.5"},
		{json.Number("7.50"), "7.50"},
		{quantity(3), "3"},
		{label("l"), "l"},
		{primitives.Experimental, "experimental"},
	}
	for _, c := range cases {
		if got := primitives.FormatValue(c.value); got != c.want {
			t.Errorf("FormatValue(%#v) = %q, want %q", c.value, got, c.want)
		}
	}
}

// TestDescribeListsArgumentsAfterTheKey 核对 Describe：没有参数时只有键，有参数时 key (name=value, …)，按名字排序
func TestDescribeListsArgumentsAfterTheKey(t *testing.T) {
	if got := primitives.Describe("orders.missing", nil); got != "orders.missing" {
		t.Errorf("without arguments: %q", got)
	}
	if got := primitives.Describe("orders.missing", map[string]any{}); got != "orders.missing" {
		t.Errorf("with empty arguments: %q", got)
	}
	got := primitives.Describe("orders.missing", map[string]any{"id": "o1", "draft": true, "owner": nil, "total": 2.0, "count": 3})
	if got != "orders.missing (count=3, draft=true, id=o1, owner=, total=2)" {
		t.Errorf("with arguments: %q", got)
	}
}

// TestMessageKeyGrammar 核对 RFC-0001 / RFC-0010 的消息键文法：首段也可含下划线，最长 128 个字符
func TestMessageKeyGrammar(t *testing.T) {
	longest := "a." + strings.Repeat("b", primitives.MaxMessageKeyLength-2)
	for _, key := range []string{"a.b", "orders.quantity.invalid", "tenant_members.list.description", "kernel.operation.invalid_payload", "a1.b_2.c3", "a_.b_", longest} {
		if !primitives.IsMessageKey(key) {
			t.Errorf("%q must be valid", key)
		}
	}
	for _, key := range []string{"", "a", "a.", ".a", "a..b", "A.b", "a.B", "_a.b", "1a.b", "a.1b", "a-b.c", "a.b-c", "a.b ", " a.b", "a.b\n", longest + "b"} {
		if primitives.IsMessageKey(key) {
			t.Errorf("%q must be invalid", key)
		}
	}
	if primitives.MessageKeyDomain("tenant_members.list.description") != "tenant_members" || primitives.MessageKeyDomain("bad") != "" {
		t.Error("domain")
	}
}

// TestDescribeSortsNamesByUTF16CodeUnits 核对 Describe 的参数按名字的序数顺序（逐个 UTF-16 码元）排列（ADR-0235）
func TestDescribeSortsNamesByUTF16CodeUnits(t *testing.T) {
	if got := primitives.Describe("orders.line.invalid", map[string]any{"b": 1, "B": 2, "a": 3}); got != "orders.line.invalid (B=2, a=3, b=1)" {
		t.Errorf("ASCII names: %q", got)
	}
	// U+FF61 的 UTF-8 字节序在 U+1F600 之前，UTF-16 码元序在它之后（代理项 0xD83D 小于 0xFF61）
	if got := primitives.Describe("orders.line.invalid", map[string]any{"｡": 1, "\U0001F600": 2}); got != "orders.line.invalid (\U0001F600=2, ｡=1)" {
		t.Errorf("names beyond the basic plane: %q", got)
	}
}
