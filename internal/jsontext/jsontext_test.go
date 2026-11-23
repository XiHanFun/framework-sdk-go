package jsontext

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestCheckFollowsTheCommonRules 核对共同检查（RFC-0001「JSON 文本」）：合法、语法错误优先、重复成员报第一处重复的路径
func TestCheckFollowsTheCommonRules(t *testing.T) {
	deep := func(levels int) string { return strings.Repeat("[", levels) + strings.Repeat("]", levels) }
	bom := string([]byte{0xEF, 0xBB, 0xBF})
	cases := []struct {
		text   string
		syntax bool
		path   string
	}{
		{`{}`, false, ""},
		{`0`, false, ""},
		{`null`, false, ""},
		{" \t\n{\"a\":1}\r\n ", false, ""},
		{`{"a":{"a":1}}`, false, ""},
		{`[{"a":1},{"a":1}]`, false, ""},
		{`{"a":1,"A":1}`, false, ""},
		{`{"😀":"😀"}`, false, ""},
		{"{\"x\":\"\U0001F600\"}", false, ""},
		{`{"a":"\u0000"}`, false, ""},
		{deep(64), false, ""},
		{``, true, "$"},
		{`   `, true, "$"},
		{`{"a":1,}`, true, "$"},
		{`[1,]`, true, "$"},
		{`{"a":1} x`, true, "$"},
		{`{} {}`, true, "$"},
		{`{"a":01}`, true, "$"},
		{`{"a":1.}`, true, "$"},
		{`{"a":.5}`, true, "$"},
		{`{"a":+1}`, true, "$"},
		{`{"a":NaN}`, true, "$"},
		{`{'a':1}`, true, "$"},
		{`{"a":1 /*c*/}`, true, "$"},
		{bom + "{}", true, "$"},
		{`{"a":"\q"}`, true, "$"},
		{"{\"a\":\"x\ty\"}", true, "$"},
		{`{"a":"\ud800"}`, true, "$"},
		{`{"a":"\udc00"}`, true, "$"},
		{`{"a":"\ud800A"}`, true, "$"},
		{`{"a":"\ud800\`, true, "$"},
		{`{"\ud800":1}`, true, "$"},
		{deep(65), true, "$"},
		{`[`, true, "$"},
		{`{"a"}`, true, "$"},
		{`tru`, true, "$"},
		{`"abc`, true, "$"},
		{`{"a":1,"a":2}`, false, "$.a"},
		{`{"a":{"b":1,"b":2}}`, false, "$.a.b"},
		{`[{"a":1,"a":2}]`, false, "$[0].a"},
		{`{"a":1,"\u0061":2}`, false, "$.a"},
		{`{"a b":1,"a b":2}`, false, "$['a b']"},
		{`{"a":1,"a":2,"b":1,"b":2}`, false, "$.a"},
		{`{"x":{"b":1,"b":2},"a":1,"a":2}`, false, "$.x.b"},
		{`{"":1,"":2}`, false, "$."},
		{`{"a":[{"b":{"c":1,"c":2}}]}`, false, "$.a[0].b.c"},
		{`{"a":1,"a":2,}`, true, "$"},
	}
	for _, c := range cases {
		problem := Check([]byte(c.text))
		switch {
		case c.path == "" && problem != nil:
			t.Errorf("%q must be valid, got %v", c.text, problem)
		case c.path != "" && (problem == nil || problem.Syntax != c.syntax || problem.Path != c.path):
			t.Errorf("%q: %+v, want syntax %v at %s", c.text, problem, c.syntax, c.path)
		}
	}
	for _, bytes := range []string{"7b2261223a22ff227d", "7b2261223a22c0af227d", "7b2261223a22eda080227d", "7b2261223a22e4b8227d", "7b22ff223a317d", "7b007d00"} {
		data, _ := hex.DecodeString(bytes)
		if problem := Check(data); problem == nil || !problem.Syntax {
			t.Errorf("%s is not valid UTF-8 JSON, got %v", bytes, problem)
		}
	}
}

// TestParseKeepsDocumentOrder 核对读出的成员按文本顺序，字符串还原转义，数字保留原文
func TestParseKeepsDocumentOrder(t *testing.T) {
	text := `{"b":1.50,"a":["x\n",true,null],"c":{"A":-0}}`
	root, problem := Parse([]byte(text))
	if problem != nil {
		t.Fatal(problem)
	}
	if len(root.Members) != 3 || root.Members[0].Name != "b" || root.Members[1].Name != "a" || root.Members[2].Name != "c" {
		t.Fatalf("members %+v", root.Members)
	}
	if number := root.Members[0].Value; number.Kind != Number || number.Text != "1.50" {
		t.Errorf("number %+v", number)
	}
	items := root.Members[1].Value.Items
	if items[0].Text != "x\n" || items[1].Kind != Bool || !items[1].Bool || items[2].Kind != Null {
		t.Errorf("items %+v", items)
	}
	inner, ok := root.Members[2].Value.Find("A")
	if !ok || inner.Text != "-0" {
		t.Errorf("inner %+v", inner)
	}
	if text[root.Members[0].Value.Start:root.Members[0].Value.End] != "1.50" || text[root.Members[1].Value.Start:root.Members[1].Value.End] != `["x\n",true,null]` {
		t.Errorf("range %d..%d", root.Members[0].Value.Start, root.Members[0].Value.End)
	}
}

// TestAppendStringEscapesOnlyWhatMustBeEscaped 核对 RFC-0010 的 JSON 字符串写法
func TestAppendStringEscapesOnlyWhatMustBeEscaped(t *testing.T) {
	cases := map[string]string{
		"q\"b\\s/\n\t\b\f\r\u0001\u001F\u007F é 中 \u2028": `"q\"b\\s/\n\t\b\f\r\u0001\u001f` + "\u007F é 中 \u2028\"",
		"\xed\xa0\x80x\xed\xbf\xbf":                       `"\ud800x\udfff"`,
		"a\xffb":                                          "\"a\uFFFDb\"",
		"\U0001F600<>&":                                   "\"\U0001F600<>&\"",
	}
	for text, want := range cases {
		if got := string(AppendString(nil, text)); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
}

// TestPaths 核对路径写法（RFC-0011 规则 2）
func TestPaths(t *testing.T) {
	cases := map[string]string{"a": "$.a", "a b": "$['a b']", "a'b": "$['a'b']", "": "$.", "a\u0085b": "$['a\u0085b']", "a-b": "$.a-b", "数量": "$.数量"}
	for name, want := range cases {
		if got := MemberPath("$", name); got != want {
			t.Errorf("%q: %s, want %s", name, got, want)
		}
	}
	if got := IndexPath("$.lines", 2); got != "$.lines[2]" {
		t.Errorf("index %s", got)
	}
}

// TestIsNumberCoversTheWholeText 核对 IsNumber：文本整个是一个 JSON 数字才算，Parse 允许的首尾空白不算
func TestIsNumberCoversTheWholeText(t *testing.T) {
	for _, text := range []string{"0", "-0", "1", "-12", "1.5", "0.0", "1e10", "1E-2", "-1.25e+3", "9007199254740993", "1e400"} {
		if !IsNumber(text) {
			t.Errorf("%q is a JSON number", text)
		}
	}
	for _, text := range []string{"", " ", " 1", "1 ", "\t1", "1\n", "+1", "01", "-", "1.", ".5", "1e", "1e+", "0x10", "1,5", "NaN", "Infinity", "-Infinity", "true", "null", `"1"`, "[1]", "1 2"} {
		if IsNumber(text) {
			t.Errorf("%q is not a JSON number", text)
		}
		// 带空白的写法 Parse 读得出数字，所以不能只看它读出的种类
		if value, problem := Parse([]byte(text)); (text == " 1" || text == "1 " || text == "\t1" || text == "1\n") && (problem != nil || value.Kind != Number) {
			t.Errorf("Parse(%q) reads a number surrounded by whitespace", text)
		}
	}
}
