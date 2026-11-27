package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestInvokeChecksInOrder 核对 invocation.md §1：未声明的操作先于就绪报 Unimplemented；声明的操作不在可服务状态时报
// plugins.process.unavailable（state 是状态名）；payload 为空等同 {}。
func TestInvokeChecksInOrder(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)
	steps := []struct {
		state primitives.LifecycleState
		next  func()
	}{
		{primitives.Declared, func() { h.handshake(ctx) }},
		{primitives.Resolved, func() { h.configure(ctx, "{}", nil) }},
		{primitives.Configured, nil},
	}
	for _, step := range steps {
		if _, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "conformance.missing"}); status.Code(err) != codes.Unimplemented {
			t.Errorf("an undeclared operation in %s must be Unimplemented, got %v", step.state, err)
		}
		invoked, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{"text":"x"}`)})
		if err != nil {
			t.Fatal(err)
		}
		expectStatus(t, "an invoke in "+step.state.String(), invoked.Status, ErrorCodeUnavailable, MessageUnavailable, map[string]any{"id": probeID, "state": step.state.String()})
		if step.next != nil {
			step.next()
		}
	}
	if started, err := h.client.Start(ctx, &pluginv1.StartRequest{}); err != nil || !started.Status.Ok {
		t.Fatalf("start %v %v", started, err)
	}
	// 空载荷读成 {}：走到处理器、报处理器自己的失败，而不是载荷不合法
	empty, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create"})
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "an empty payload", empty.Status, "PROBE_VALIDATION_001", "probe.text.missing", nil)
}

// TestInvokeReportsResponsesThatCannotBeWritten 核对 invocation.md §1「插件一侧写出应答」与 RFC-0011 规则 10「编码」（ADR-0318）：
// 类型化的响应里的字符串（值、成员名、数组的项）不是合法的 UTF-8（含编成 UTF-8 的孤立代理项）时应答写不出，Status.error 为
// KERNEL_UNEXPECTED_001 + kernel.invocation.unhandled_exception，没有参数、不带 inner，payload 为空，不写成 U+FFFD 交给宿主；
// 多字节文本与真正的 U+FFFD 照常写出。
func TestInvokeReportsResponsesThatCannotBeWritten(t *testing.T) {
	responses := map[string]any{
		"value":     map[string]any{"text": "a\xed\xa0\x80"},
		"member":    map[string]any{"\xff": "x"},
		"element":   []any{"ok", "\xed\xb0\x80"},
		"multibyte": map[string]any{"text": "汉字\U0001F600\xef\xbf\xbd"},
	}
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
		registry := operation.NewRegistry()
		echo := operation.HandlerFunc[echoRequest, any](func(_ *invocation.Context, request echoRequest) primitives.Result[any] {
			return primitives.Ok(responses[request.Text])
		})
		if err := operation.Add(registry, operation.Contract{ID: "echoes.create", Stability: primitives.Experimental}, echo, nil, ""); err != nil {
			t.Fatal(err)
		}
		opts.Operations = registry
	})
	ctx := deadline(t)
	h.ready(ctx, nil)
	invoke := func(text string) *pluginv1.InvokeResponse {
		t.Helper()
		response, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "echoes.create", Payload: []byte(`{"text":"` + text + `"}`)})
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	for _, text := range []string{"value", "member", "element"} {
		response := invoke(text)
		expectStatus(t, "a response with a "+text+" that is not valid UTF-8", response.Status, operation.ErrorCodeUnexpected, invocation.MessageUnhandledException, nil)
		if response.Status.GetError().GetInner() != nil || len(response.Payload) != 0 {
			t.Errorf("%s: no inner error and no payload, got %v %q", text, response.Status.GetError().GetInner(), response.Payload)
		}
	}
	if written := invoke("multibyte"); !written.Status.GetOk() || string(written.Payload) != "{\"text\":\"汉字\U0001F600\xef\xbf\xbd\"}" {
		t.Errorf("multibyte text and U+FFFD are written as they are, got %v %q", written.Status, written.Payload)
	}
}

// TestInvokeStreamIsNotImplemented 核对 invocation.md §1：读第一帧，第一帧之前结束或第一帧不是 start 为 InvalidArgument，
// 是 start 为 Unimplemented，不论操作是否声明、插件是否就绪。
func TestInvokeStreamIsNotImplemented(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)
	open := func(first *pluginv1.StreamFrame) error {
		t.Helper()
		stream, err := h.client.InvokeStream(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			_ = stream.CloseSend()
		} else if err := stream.Send(first); err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		_, err = stream.Recv()
		return err
	}
	starting := func(operation string) *pluginv1.StreamFrame {
		return &pluginv1.StreamFrame{Frame: &pluginv1.StreamFrame_Start{Start: &pluginv1.InvokeRequest{Operation: operation, Payload: []byte("{}")}}}
	}
	check := func(when string) {
		t.Helper()
		if err := open(nil); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: a stream closed before its first frame must be InvalidArgument, got %v", when, err)
		}
		if err := open(&pluginv1.StreamFrame{Frame: &pluginv1.StreamFrame_Chunk{Chunk: []byte("{}")}}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: a stream starting with a chunk must be InvalidArgument, got %v", when, err)
		}
		if err := open(&pluginv1.StreamFrame{Frame: &pluginv1.StreamFrame_End{End: &pluginv1.Status{Ok: true}}}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: a stream starting with end must be InvalidArgument, got %v", when, err)
		}
		for _, operation := range []string{"echoes.create", "conformance.missing"} {
			if err := open(starting(operation)); status.Code(err) != codes.Unimplemented {
				t.Errorf("%s: a stream starting %s must be Unimplemented, got %v", when, operation, err)
			}
		}
	}
	check("before the handshake")
	h.ready(ctx, nil)
	check("when ready")
}

// TestContextIsRestoredFromMetadata 核对 invocation.md §2：trace_parent 不合文法即没有父 Span；baggage 只留已登记的键（含 xihan.principal）；
// deadline 读不出时仍用 gRPC 的截止时间，两者取较早的。
func TestContextIsRestoredFromMetadata(t *testing.T) {
	ctx := FromMetadata(context.Background(), &pluginv1.InvocationMetadata{
		TraceParent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		TraceState:  "vendor=1",
		Baggage:     map[string]string{"xihan.tenant": "t1", "xihan.principal": "p1", "xihan.custom": "c", "bad key": "x", "xihan.correlation": ""},
	})
	if ctx.TraceID() != "0af7651916cd43dd8448eb211c80319c" || ctx.SpanID() != "b7ad6b7169203331" || ctx.Tenant() != "t1" || ctx.Principal() != "p1" {
		t.Errorf("context %s %s %q %q", ctx.TraceID(), ctx.SpanID(), ctx.Tenant(), ctx.Principal())
	}
	for _, key := range []string{"xihan.custom", "bad key", "xihan.correlation"} {
		if _, ok := ctx.GetBaggage(key); ok {
			t.Errorf("the baggage key %q must not enter the context", key)
		}
	}
	if written := ToMetadata(ctx); written.TraceState != "" || written.Baggage["xihan.tenant"] != "t1" {
		t.Errorf("metadata %+v", written)
	}
	for _, invalid := range []string{"01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "00-0AF7651916CD43DD8448EB211C80319C-b7ad6b7169203331-01", "garbage"} {
		if derived := FromMetadata(context.Background(), &pluginv1.InvocationMetadata{TraceParent: invalid}); derived.TraceID() == "0af7651916cd43dd8448eb211c80319c" || !isRoot(derived) {
			t.Errorf("an invalid traceparent %q must give no parent span", invalid)
		}
	}

	// 元数据的截止时间写法不对时仍有 gRPC 的截止时间（旧版连它也丢了）
	grpcDeadline := time.Now().Add(time.Minute).UTC()
	parent, cancel := context.WithDeadline(context.Background(), grpcDeadline)
	defer cancel()
	if got, ok := FromMetadata(parent, &pluginv1.InvocationMetadata{Deadline: "2026-11-18 03:25:00Z"}).Deadline(); !ok || !got.Equal(grpcDeadline) {
		t.Errorf("an unreadable deadline falls back to the gRPC deadline, got %v %v", got, ok)
	}
	earlier := grpcDeadline.Add(-30 * time.Second)
	if got, _ := FromMetadata(parent, &pluginv1.InvocationMetadata{Deadline: rules.FormatDeadline(earlier)}).Deadline(); !got.Equal(earlier.Truncate(100 * time.Nanosecond)) {
		t.Errorf("the earlier deadline wins, got %v want %v", got, earlier)
	}
	later := grpcDeadline.Add(30 * time.Second)
	if got, _ := FromMetadata(parent, &pluginv1.InvocationMetadata{Deadline: later.Format(time.RFC3339Nano)}).Deadline(); !got.Equal(grpcDeadline) {
		t.Errorf("the gRPC deadline wins when it is earlier, got %v", got)
	}
	if _, ok := FromMetadata(context.Background(), nil).Deadline(); ok {
		t.Error("no metadata and no gRPC deadline means no deadline")
	}
}

// isRoot 判断上下文没有上游的追踪（RFC-0003 规则 7）。
func isRoot(ctx *invocation.Context) bool {
	_, has := ctx.TraceFlags()
	return !has
}

// TestErrorsAreWrittenWithoutRenderedText 核对 invocation.md §3「写出」：带消息键的错误 text 留空，只有自由文本错误写 text；
// details 是 JSON 对象文本，没有时为空；inner 照写。
func TestErrorsAreWrittenWithoutRenderedText(t *testing.T) {
	inner := primitives.Literal("KERNEL_UNEXPECTED_001", "boom", nil, nil)
	keyed := primitives.Unexpected("ORDERS_UNEXPECTED_001", "orders.failed", map[string]any{"id": "o1"}, inner).WithText("rendered")
	encoded := ToProtoError(keyed)
	if encoded.Code != "ORDERS_UNEXPECTED_001" || encoded.MessageKey != "orders.failed" || encoded.Text != "" || encoded.Details != `{"id":"o1"}` {
		t.Errorf("keyed %+v", encoded)
	}
	if encoded.Inner == nil || encoded.Inner.Text != "boom" || encoded.Inner.MessageKey != "" || encoded.Inner.Details != "" {
		t.Errorf("inner %+v", encoded.Inner)
	}
	if plain := ToProtoError(primitives.NotFound("ORDERS_NOTFOUND_001", "orders.missing", nil)); plain.Details != "" || plain.Text != "" {
		t.Errorf("an error without details writes empty details, got %+v", plain)
	}
	if ToStatus(primitives.Success()).Error != nil || !ToStatus(primitives.Success()).Ok {
		t.Error("success")
	}
}

// TestErrorDetailsThatCannotBeWrittenAreLeftOut 核对 invocation.md §3「写出」与 RFC-0001「写出与转交」（ADR-0320）：细节里的字符串
// （成员名与值）不是合法的 UTF-8（含编成 UTF-8 的孤立代理项），或写出的文本没过共同检查（嵌套超过 64 层、重复成员、原样嵌进去的
// 孤立代理项转义），这个错误就不带细节，code、message_key、text 与 inner 照写；内部错误的细节各自核对；多字节文本、真正的 U+FFFD
// 与正好 64 层照常写出
func TestErrorDetailsThatCannotBeWrittenAreLeftOut(t *testing.T) {
	deep := func(depth int) any {
		var value any = 0
		for range depth {
			value = []any{value}
		}
		return value
	}
	for name, details := range map[string]map[string]any{
		"a surrogate in a value":     {"id": "o\xed\xa0\x80"},
		"an invalid byte in a value": {"ids": []any{"ok", "\xff"}},
		"a surrogate in a name":      {"i\xed\xb0\x80d": 1},
		"an invalid byte in a name":  {"a": map[string]any{"\xff": 1}},
		"a raw duplicate":            {"raw": json.RawMessage(`{"a":1,"a":2}`)},
		"a raw surrogate escape":     {"raw": json.RawMessage(`"\ud800"`)},
		"65 levels":                  {"deep": deep(64)},
	} {
		inner := primitives.Literal("KERNEL_UNEXPECTED_001", "boom", map[string]any{"id": "i1"}, nil)
		encoded := ToProtoError(primitives.Unexpected("ORDERS_UNEXPECTED_001", "orders.failed", details, inner))
		if encoded.Code != "ORDERS_UNEXPECTED_001" || encoded.MessageKey != "orders.failed" || encoded.Text != "" || encoded.Details != "" {
			t.Errorf("%s: the error is written without details, got %+v", name, encoded)
		}
		if encoded.Inner == nil || encoded.Inner.Text != "boom" || encoded.Inner.Details != `{"id":"i1"}` {
			t.Errorf("%s: the inner error is written with its own details, got %+v", name, encoded.Inner)
		}
		literal := ToProtoError(primitives.Literal("ORDERS_CONFLICT_001", "stale", nil, primitives.Literal("KERNEL_UNEXPECTED_001", "boom", details, nil)))
		if literal.Text != "stale" || literal.Inner == nil || literal.Inner.Text != "boom" || literal.Inner.Details != "" {
			t.Errorf("%s: only the inner error loses its details, got %+v / %+v", name, literal, literal.Inner)
		}
	}
	writable := ToProtoError(primitives.Literal("ORDERS_CONFLICT_001", "stale", map[string]any{"r": "\xef\xbf\xbd", "t": "汉字\U0001F600", "deep": deep(63)}, nil))
	if want := "{\"deep\":" + strings.Repeat("[", 63) + "0" + strings.Repeat("]", 63) + ",\"r\":\"\xef\xbf\xbd\",\"t\":\"汉字\U0001F600\"}"; writable.Details != want {
		t.Errorf("details that pass the checks are written, got %q, want %q", writable.Details, want)
	}
}

// TestErrorTextIsWrittenWithMaximalSubpartsReplaced 核对 invocation.md §3「写出 `Error` 的文案」（ADR-0339）：text（含内部错误的）
// 编不成合法 UTF-8 的部分按 Unicode §3.9 的最大子部分换成 U+FFFD，每一段一个；合法的多字节字符与本来就是 U+FFFD 的照抄；
// code、message_key、details 与 inner 照写，整条错误照常编码。期望值与 .NET 的 Encoding.UTF8.GetString、Python 的
// bytes.decode('utf-8', 'replace') 相同
func TestErrorTextIsWrittenWithMaximalSubpartsReplaced(t *testing.T) {
	const r = "\uFFFD"
	for _, c := range []struct{ name, text, want string }{
		{"a lone high surrogate", "a\xed\xa0\x80b", "a" + r + r + r + "b"},
		{"a lone low surrogate", "\xed\xb0\x80", r + r + r},
		{"a surrogate pair encoded separately", "\xed\xa0\xbd\xed\xb8\x80", r + r + r + r + r + r},
		{"a supplementary character", "x\U0001F600y", "x\U0001F600y"},
		{"a real U+FFFD", "\xef\xbf\xbd", r},
		{"multibyte text", "汉字 é", "汉字 é"},
		{"a truncated four-byte sequence before ASCII", "\xf0\x9f\x98b", r + "b"},
		{"an overlong slash", "\xc0\xaf", r + r},
		{"a truncated three-byte sequence at the end", "x\xe2\x82", "x" + r},
		{"a truncated two-byte sequence at the end", "\xc2", r},
		{"a truncated two-byte sequence before ASCII", "\xc2A", r + "A"},
		{"E0 with a second byte out of range", "\xe0\x80", r + r},
		{"F4 with a second byte out of range", "\xf4\x90", r + r},
		{"ED with a second byte out of range", "\xed\xa0", r + r},
		{"F0 9F before an out-of-range third byte", "\xf0\x9fA", r + "A"},
		{"a lone continuation byte", "\x80", r},
		{"bytes that never start a sequence", "\xf5\xff\xfe", r + r + r},
		{"a truncated sequence before a valid one", "\xe2\x82\xe2\x82\xac", r + "\u20AC"},
	} {
		details := map[string]any{"id": "o1"}
		inner := primitives.Literal("KERNEL_UNEXPECTED_001", "inner "+c.text, map[string]any{"n": 1}, nil)
		encoded := ToProtoError(primitives.Literal("ORDERS_CONFLICT_001", c.text, details, inner))
		if encoded.Code != "ORDERS_CONFLICT_001" || encoded.MessageKey != "" || encoded.Text != c.want || encoded.Details != `{"id":"o1"}` {
			t.Errorf("%s: got %+v, want the text %q", c.name, encoded, c.want)
		}
		if encoded.Inner == nil || encoded.Inner.Code != "KERNEL_UNEXPECTED_001" || encoded.Inner.Text != "inner "+c.want || encoded.Inner.Details != `{"n":1}` {
			t.Errorf("%s: the inner error's text is replaced the same way, got %+v", c.name, encoded.Inner)
		}
		// 以前照写非法的 UTF-8，protobuf-go 拒绝编码整条消息，回调、发布与应答都写不出
		wire, err := proto.Marshal(&pluginv1.Status{Ok: false, Error: encoded})
		if err != nil {
			t.Errorf("%s: the whole error is written, got %v", c.name, err)
			continue
		}
		var decoded pluginv1.Status
		if err := proto.Unmarshal(wire, &decoded); err != nil || decoded.GetError().GetText() != c.want || decoded.GetError().GetInner().GetText() != "inner "+c.want {
			t.Errorf("%s: the host reads the replaced text, got %+v (%v)", c.name, decoded.GetError(), err)
		}
	}
	// 带消息键的错误不写 text，WithText 填进去的文案即使编不成 UTF-8 也不出现
	keyed := ToProtoError(primitives.Unexpected("ORDERS_UNEXPECTED_001", "orders.failed", nil, nil).WithText("x\xff"))
	if keyed.Text != "" || keyed.MessageKey != "orders.failed" {
		t.Errorf("a keyed error writes no text, got %+v", keyed)
	}
}

// TestHostErrorsAreReadByTheRules 核对 invocation.md §3「读入」（插件读宿主）：码不合文法换成 plugins.process.failed（phase 为 invoke）；
// 键合文法按键处理，否则为自由文本（text 空则 code）；details 不合法或不是对象当作没有；inner 同此；ok 为假却没有 error 另有 reason。
func TestHostErrorsAreReadByTheRules(t *testing.T) {
	invalid := FromProtoError(&pluginv1.Error{Code: "bad", MessageKey: "orders.failed", Details: `{"id":"o1"}`}, probeID)
	expectError(t, "an invalid code", invalid, ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "invoke", "reason": "invalid error code 'bad'"})

	keyed := FromProtoError(&pluginv1.Error{Code: "ORDERS_UNEXPECTED_001", MessageKey: "orders.failed", Text: "ignored", Details: `{"id":"o1"}`}, probeID)
	if keyed.MessageKey != "orders.failed" || keyed.Text != "" || keyed.Details["id"] != "o1" {
		t.Errorf("keyed %+v", keyed)
	}
	literal := FromProtoError(&pluginv1.Error{Code: "ORDERS_UNEXPECTED_001", MessageKey: "Not A Key", Text: "free text"}, probeID)
	if literal.MessageKey != "" || literal.Message() != "free text" {
		t.Errorf("an invalid key makes a free-text error, got %+v", literal)
	}
	if empty := FromProtoError(&pluginv1.Error{Code: "ORDERS_UNEXPECTED_001"}, probeID); empty.MessageKey != "" || empty.Message() != "ORDERS_UNEXPECTED_001" {
		t.Errorf("a free-text error without text takes the code, got %+v", empty)
	}
	// 空对象、只有空白、有读成双精度会溢出的数也当作没有细节；错误码不合法时原来的 inner 丢掉
	if dropped := FromProtoError(&pluginv1.Error{Code: "bad", Inner: &pluginv1.Error{Code: "ORDERS_UNEXPECTED_001", Text: "inner"}}, probeID); dropped.Inner != nil {
		t.Errorf("an invalid code drops the inner error, got %+v", dropped.Inner)
	}
	for _, details := range []string{`{"a":1,"a":2}`, `[1]`, `"x"`, `{"a":`, "null", "{}", " ", `{"a":1e309}`, `{"a":[-1e309]}`} {
		if decoded := FromProtoError(&pluginv1.Error{Code: "ORDERS_UNEXPECTED_001", MessageKey: "orders.failed", Details: details}, probeID); decoded.Details != nil {
			t.Errorf("details %s must be dropped, got %#v", details, decoded.Details)
		}
	}
	nested := FromProtoError(&pluginv1.Error{Code: "ORDERS_UNEXPECTED_001", MessageKey: "orders.failed", Inner: &pluginv1.Error{Code: "nope"}}, probeID)
	expectError(t, "an invalid inner code", nested.Inner, ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "invoke", "reason": "invalid error code 'nope'"})

	for _, broken := range []*pluginv1.Status{nil, {}, {Ok: false}} {
		expectError(t, "a failure without an error", FromStatus(broken, probeID, "orders.create").Err(), ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": probeID, "phase": "orders.create", "reason": "the host reported a failure without an error"})
	}
	if !FromStatus(&pluginv1.Status{Ok: true}, probeID, "publish").OK() {
		t.Error("ok")
	}
}

// TestHandleEventChecksInOrder 核对 invocation.md §4：没有信封或方式不合法为 InvalidArgument；找不到订阅（即使还没就绪）为
// KERNEL_NOTFOUND_001 + plugins.event.unsubscribed；有订阅而不可服务为 plugins.process.unavailable。
func TestHandleEventChecksInOrder(t *testing.T) {
	h := start(t, defaultEnv())
	ctx := deadline(t)
	envelope := func(topic, messageType string) *pluginv1.Envelope {
		return &pluginv1.Envelope{Topic: topic, Id: "e1", Type: messageType, SpecVersion: "1.0"}
	}
	for _, request := range []*pluginv1.HandleEventRequest{
		{Mode: "competing"},
		{Envelope: envelope("probe.events", "probe.created"), Mode: "bogus"},
		{Envelope: envelope("probe.events", "probe.created"), Mode: "Competing"},
	} {
		if _, err := h.client.HandleEvent(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Errorf("delivery %v must be InvalidArgument, got %v", request, err)
		}
	}
	for _, mode := range []string{"", "competing"} {
		delivered, err := h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: envelope("other.events", "other.created"), Mode: mode, Attempt: 1})
		if err != nil {
			t.Fatal(err)
		}
		expectStatus(t, "an unsubscribed delivery in mode "+mode, delivered.Status, operation.ErrorCodeNotFound, MessageUnsubscribed, map[string]any{"id": probeID, "topic": "other.events", "type": "other.created", "mode": "competing"})
	}
	// 以广播投递只有竞争订阅的类型：没有以这种方式声明的订阅，不改交给竞争处理器
	broadcast, err := h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: envelope("probe.events", "probe.created"), Mode: "broadcast", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "a broadcast delivery without a broadcast subscription", broadcast.Status, operation.ErrorCodeNotFound, MessageUnsubscribed, map[string]any{"id": probeID, "topic": "probe.events", "type": "probe.created", "mode": "broadcast"})
	early, err := h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: envelope("probe.events", "probe.created"), Mode: "competing", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "a delivery before Start", early.Status, ErrorCodeUnavailable, MessageUnavailable, map[string]any{"id": probeID, "state": "Declared"})
	if events := read(h.seen, func() []string { return h.seen.events }); len(events) != 0 {
		t.Errorf("no handler may run, got %v", events)
	}
}

// TestHandleEventRunsEverySubscription 核对 invocation.md §4 第 5 条：匹配的订阅按声明顺序依次执行，第一个失败即结果；
// 处理器 panic 为 KERNEL_UNEXPECTED_001 + kernel.invocation.unhandled_exception。
func TestHandleEventRunsEverySubscription(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	failing := primitives.Unavailable("PROBE_UNAVAILABLE_001", "probe.backend.down", nil)
	handler := func(name string, outcome func() primitives.Result[primitives.Unit]) SubscriptionHandler {
		return SubscriptionHandler{Subscription: Subscription{Topic: "probe.events", MessageType: map[string]string{"any": "", "typed": "probe.created", "panics": "probe.exploded"}[name]}, Handler: func(_ *invocation.Context, envelope Envelope, _ uint32) primitives.Result[primitives.Unit] {
			mu.Lock()
			calls = append(calls, name+":"+envelope.ID)
			mu.Unlock()
			return outcome()
		}}
	}
	var failTyped atomic.Bool
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
		// （t，空）与（t，x）都是竞争订阅：类型 x 的一次竞争投递先后交给两者
		opts.Subscriptions = []SubscriptionHandler{
			handler("any", func() primitives.Result[primitives.Unit] {
				if failTyped.Load() {
					return primitives.Fail[primitives.Unit](failing)
				}
				return primitives.Success()
			}),
			handler("typed", func() primitives.Result[primitives.Unit] { return primitives.Success() }),
			handler("panics", func() primitives.Result[primitives.Unit] { panic(errors.New("boom")) }),
		}
	})
	ctx := deadline(t)
	h.ready(ctx, nil)
	deliver := func(id, messageType string) *pluginv1.Status {
		t.Helper()
		response, err := h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: &pluginv1.Envelope{Topic: "probe.events", Id: id, Type: messageType, SpecVersion: "1.0"}, Mode: "competing", Attempt: 1})
		if err != nil {
			t.Fatal(err)
		}
		return response.Status
	}
	if outcome := deliver("e1", "probe.created"); !outcome.Ok {
		t.Errorf("e1 %v", outcome)
	}
	failTyped.Store(true)
	expectStatus(t, "the first failure", deliver("e2", "probe.created"), "PROBE_UNAVAILABLE_001", "probe.backend.down", nil)
	failTyped.Store(false)
	expectStatus(t, "a panicking handler", deliver("e3", "probe.exploded"), operation.ErrorCodeUnexpected, invocation.MessageUnhandledException, nil)
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"any:e1", "typed:e1", "any:e2", "any:e3", "panics:e3"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
}
