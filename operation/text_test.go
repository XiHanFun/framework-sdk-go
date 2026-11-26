package operation_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// textMarshaled 的 MarshalText 原样交出它自己的字节
type textMarshaled string

// MarshalText 实现 encoding.TextMarshaler。
func (t textMarshaled) MarshalText() ([]byte, error) { return []byte(t), nil }

// codeKey 是整数种类、以 MarshalText 写出的映射键：交出的字节不是合法的 UTF-8
type codeKey int

// MarshalText 实现 encoding.TextMarshaler。
func (codeKey) MarshalText() ([]byte, error) { return []byte{'k', 0xFF}, nil }

// rawMarshaled 的 MarshalJSON 原样交出它自己的文本
type rawMarshaled string

// MarshalJSON 实现 json.Marshaler。
func (r rawMarshaled) MarshalJSON() ([]byte, error) { return []byte(r), nil }

// hidden 以 MarshalJSON 写出固定的文本，字段不写出
type hidden struct{ Text string }

// MarshalJSON 实现 json.Marshaler。
func (hidden) MarshalJSON() ([]byte, error) { return []byte(`"hidden"`), nil }

// pointerMarshaled 只有指针接收者的 MarshalJSON：encoding/json 只在值可寻址时用它，否则按字段写
type pointerMarshaled struct {
	Text string `json:"text"`
}

// MarshalJSON 实现 json.Marshaler。
func (*pointerMarshaled) MarshalJSON() ([]byte, error) { return []byte(`"pointer"`), nil }

// alwaysZero 的 IsZero 总为真：带 omitzero 的字段不写出
type alwaysZero struct {
	Text string `json:"text"`
}

// IsZero 让 omitzero 略去它。
func (alwaysZero) IsZero() bool { return true }

// leftName 与 rightName 在同一层提升出同名的字段：encoding/json 两个都不写
type leftName struct{ Name string }

// rightName 见 leftName。
type rightName struct{ Name string }

// pointerZero 只有指针接收者的 IsZero：omitzero 照样用它，值不可寻址时 encoding/json 先复制一份再取地址
type pointerZero struct {
	Text string `json:"text"`
}

// IsZero 让 omitzero 略去它。
func (*pointerZero) IsZero() bool { return true }

// omitted 的成员都不写出：同层同名的提升字段、json:"-"、未导出的字段、omitzero 略去的字段
type omitted struct {
	leftName
	rightName
	Skipped     string `json:"-"`
	private     string
	Zero        alwaysZero  `json:"zero,omitzero"`
	PointerZero pointerZero `json:"pointerZero,omitzero"`
	Kept        string      `json:"kept"`
	Optional    *string     `json:"optional,omitempty"`
}

// promotedText 是未导出的嵌入类型：它的导出字段提升到外层，照常写出
type promotedText struct{ Text string }

// textTag 是类型化标识的标签
type textTag struct{}

// nested 套 depth 层数组，最里面是 0
func nested(depth int) any {
	var value any = 0
	for range depth {
		value = []any{value}
	}
	return value
}

// TestResponsesThatAreNotJSONTextAreNotWritten 核对 RFC-0011 规则 10「编码」与 RFC-0001「写出与转交」（ADR-0318）：类型化的响应写不成
// 合法的 JSON 文本即写不出——字符串的值、成员名（映射的键）、数组的项、MarshalText 的结果与 primitives.ID / Maybe 里不是合法的 UTF-8
// （含编成 UTF-8 的孤立代理项），json.Marshaler 交出的原文里有非法的字节、孤立代理项的转义或重复的成员，嵌套超过 64 层——都以
// KERNEL_UNEXPECTED_001 + kernel.invocation.unhandled_exception 结束，没有参数、不带 inner、没有载荷，不写成 U+FFFD；
// encoding/json 不写出的值（MarshalJSON 自己写的、同层同名的提升字段、json:"-"、未导出的、omitzero 略去的）不看，
// 多字节文本与真正的 U+FFFD 照常写出
func TestResponsesThatAreNotJSONTextAreNotWritten(t *testing.T) {
	// Execute 不经调用器，原因写进 slog.Default
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	registry := operation.NewRegistry()
	unwritable := map[string]any{
		"texts.surrogate":       map[string]string{"text": "a\xed\xa0\x80"},
		"texts.invalid":         struct{ Text string }{Text: "\xff"},
		"texts.key":             map[string]int{"\xff": 1},
		"texts.surrogate-key":   map[string]int{"ok": 1, "a\xed\xb0\x80": 2},
		"texts.element":         []string{"ok", "\xff"},
		"texts.nested":          struct{ Items []any }{Items: []any{map[string]any{"deep": &struct{ Name string }{Name: "\xed\xbf\xbf"}}}},
		"texts.text-marshaler":  struct{ Code textMarshaled }{Code: "\xff"},
		"texts.text-key":        map[codeKey]int{1: 1},
		"texts.raw-bytes":       rawMarshaled("\"\xff\""),
		"texts.raw-escape":      rawMarshaled(`"\ud800"`),
		"texts.raw-message":     map[string]json.RawMessage{"raw": json.RawMessage(`{"a":"\udc00x"}`)},
		"texts.raw-duplicate":   rawMarshaled(`{"a":1,"a":2}`),
		"texts.id":              primitives.NewID[textTag]("\xff"),
		"texts.maybe":           struct{ Note primitives.Maybe[string] }{Note: primitives.Some("\xff")},
		"texts.not-addressable": pointerMarshaled{Text: "\xff"},
		"texts.too-deep":        nested(65),
		"texts.promoted":        struct{ promotedText }{promotedText{Text: "\xff"}},
		// Error 与 Result 的线上编码自己写不出（ADR-0320）同样是写不成合法的 JSON 文本，不带 inner
		"texts.error":  struct{ Failure *primitives.Error }{primitives.Literal("ORDERS_CONFLICT_001", "a\xed\xa0\x80", nil, nil)},
		"texts.result": primitives.Ok(map[string]string{"text": "\xff"}),
	}
	for id, response := range unwritable {
		register(t, registry, id, response)
	}
	writable := map[string]struct {
		response any
		payload  string
	}{
		"texts.multibyte":   {map[string]string{"text": "汉字\U0001F600", "replacement": "\xef\xbf\xbd"}, "{\"replacement\":\"\xef\xbf\xbd\",\"text\":\"汉字\U0001F600\"}"},
		"texts.hidden":      {hidden{Text: "\xff"}, `"hidden"`},
		"texts.addressable": {[]pointerMarshaled{{Text: "\xff"}}, `["pointer"]`},
		"texts.pointer":     {&pointerMarshaled{Text: "\xff"}, `"pointer"`},
		"texts.omitted":     {omitted{leftName: leftName{Name: "\xff"}, rightName: rightName{Name: "\xff"}, Skipped: "\xff", private: "\xff", Zero: alwaysZero{Text: "\xff"}, PointerZero: pointerZero{Text: "\xff"}, Kept: "k"}, `{"kept":"k"}`},
		"texts.maybe-none":  {primitives.None[string](), `null`},
		"texts.deep-enough": {nested(64), strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64)},
	}
	for id, c := range writable {
		register(t, registry, id, c.response)
	}
	var log bytes.Buffer
	invoker, err := operation.NewInvoker(registry, nil, operation.WithLogger(slog.New(slog.NewTextHandler(&log, nil))))
	if err != nil {
		t.Fatal(err)
	}

	for id := range unwritable {
		response := invoker.Invoke(invocation.Background(), operation.Request{ID: id})
		assertError(t, response.Outcome.Err(), "KERNEL_UNEXPECTED_001", "kernel.invocation.unhandled_exception", nil, "An unhandled exception occurred during the invocation.")
		if response.Payload != nil || (response.Outcome.Err() != nil && response.Outcome.Err().Inner != nil) {
			t.Errorf("%s: no payload and no inner error, got %q %v", id, response.Payload, response.Outcome.Err())
		}
		descriptor, _ := registry.Get(id)
		if executed := descriptor.Execute(invocation.Background(), nil); executed.Outcome.Err() == nil || executed.Outcome.Err().MessageKey != invocation.MessageUnhandledException || executed.Payload != nil {
			t.Errorf("%s: Execute encodes the same way, got %v %q", id, executed.Outcome, executed.Payload)
		}
	}
	// 写不出的原因进调用器的日志，写明位置，供插件作者排查
	if !strings.Contains(log.String(), "$[1]") || !strings.Contains(log.String(), "$.Items[0].deep.Name") {
		t.Errorf("the log must locate the string that is not valid UTF-8:\n%s", log.String())
	}
	for id, c := range writable {
		response := invoker.Invoke(invocation.Background(), operation.Request{ID: id})
		if !response.Outcome.OK() || string(response.Payload) != c.payload {
			t.Errorf("%s: got %v %q, want %q", id, response.Outcome, response.Payload, c.payload)
		}
	}
}

// TestNestingIsCountedOverTheWholeText 核对嵌套层数按写出的整段文本计（RFC-0011 规则 10「编码」，ADR-0319）：json.Marshaler 交出的
// 60 层原文套在 10 层数组里，两段各自都不超过 64 层，合起来 70 层即写不出，以 KERNEL_UNEXPECTED_001 + unhandled_exception 结束、
// 没有载荷；同样的 60 层套在 4 层里正好 64 层，照常写出
func TestNestingIsCountedOverTheWholeText(t *testing.T) {
	raw := rawMarshaled(strings.Repeat("[", 60) + "0" + strings.Repeat("]", 60))
	within := func(depth int) any {
		var value any = raw
		for range depth {
			value = []any{value}
		}
		return value
	}
	registry := operation.NewRegistry()
	register(t, registry, "texts.raw-within-ten", within(10))
	register(t, registry, "texts.raw-within-four", within(4))
	invoker, err := operation.NewInvoker(registry, nil, operation.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}

	response := invoker.Invoke(invocation.Background(), operation.Request{ID: "texts.raw-within-ten"})
	assertError(t, response.Outcome.Err(), "KERNEL_UNEXPECTED_001", "kernel.invocation.unhandled_exception", nil, "An unhandled exception occurred during the invocation.")
	if response.Payload != nil {
		t.Errorf("70 levels must not be written, got %q", response.Payload)
	}
	response = invoker.Invoke(invocation.Background(), operation.Request{ID: "texts.raw-within-four"})
	if want := strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64); !response.Outcome.OK() || string(response.Payload) != want {
		t.Errorf("64 levels must be written, got %v %q", response.Outcome, response.Payload)
	}
}
