package plugin

import (
	"context"
	"encoding/json"
	"io/fs"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-spec/conformance"
	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/invocation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestHandshakeLinesFollowTheVectors 回放 handshake-lines.json（handshake.md §2）：SDK 的 ParseEndpoint 解析前缀之后的部分；
// SDK 写出的握手行也合文法。
func TestHandshakeLinesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.HandshakeLines()
	if err != nil {
		t.Fatal(err)
	}
	parse := func(line string) (Endpoint, bool) {
		rest, ok := strings.CutPrefix(line, HandshakeLineVersion+" ")
		if !ok {
			return Endpoint{}, false
		}
		return ParseEndpoint(rest)
	}
	for _, c := range vectors.Valid {
		endpoint, ok := parse(c.Line)
		if !ok || string(endpoint.Network) != c.Network || endpoint.Address != c.Address {
			t.Errorf("%q parsed as %+v (ok=%v), want %s %s", c.Line, endpoint, ok, c.Network, c.Address)
			continue
		}
		if written := handshakeLine(endpoint); written != c.Line {
			t.Errorf("%q written back as %q", c.Line, written)
		}
	}
	for _, line := range vectors.Invalid {
		if endpoint, ok := parse(line); ok {
			t.Errorf("%q must not parse, got %+v", line, endpoint)
		}
	}
}

// TestHostAddressesFollowTheVectors 回放 host-addresses.json（handshake.md §4）：合法的值逐项可解析，不合法的值整个不合法，
// 选择取第一个本 SDK 能连的 network；本平台能连的 network 按 callbackNetworks。
func TestHostAddressesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.HostAddresses()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Valid {
		// 每个合法值的第一项就是只支持它的 network 时选中的项
		selected, ok := selectHostEndpoint(c.Value, []Network{Network(c.Entries[0].Network)})
		if !ok || string(selected.Network) != c.Entries[0].Network || selected.Address != c.Entries[0].Address {
			t.Errorf("%q selected %+v (ok=%v), want %+v", c.Value, selected, ok, c.Entries[0])
		}
		for _, entry := range c.Entries {
			if endpoint, ok := ParseEndpoint(entry.Network + " " + entry.Address); !ok || endpoint.Address != entry.Address {
				t.Errorf("entry %+v of %q must parse", entry, c.Value)
			}
		}
	}
	for _, value := range vectors.Invalid {
		if selected, ok := selectHostEndpoint(value, []Network{NetworkUnix, NetworkTCP, NetworkPipe}); ok {
			t.Errorf("%q must not select anything, got %+v", value, selected)
		}
	}
	for _, c := range vectors.Selection {
		value := ""
		if c.Value != nil {
			value = *c.Value
		}
		supported := make([]Network, 0, len(c.Supported))
		for _, network := range c.Supported {
			supported = append(supported, Network(network))
		}
		selected, ok := selectHostEndpoint(value, supported)
		switch {
		case c.Selected == nil && ok:
			t.Errorf("%q with %v must select nothing, got %+v", value, c.Supported, selected)
		case c.Selected != nil && (!ok || string(selected.Network) != c.Selected.Network || selected.Address != c.Selected.Address):
			t.Errorf("%q with %v selected %+v (ok=%v), want %+v", value, c.Supported, selected, ok, *c.Selected)
		}
	}
	// 本平台：宿主在 Windows 上给命名管道加回环 TCP，其他系统给 Unix 域套接字加回环 TCP，本 SDK 都取第一项
	for _, value := range []string{`npipe \\.\pipe\xihan-host;tcp 127.0.0.1:50123`, "unix /tmp/xihan-host/host.sock;tcp 127.0.0.1:50123"} {
		selected, ok := selectHostEndpoint(value, callbackNetworks)
		first := strings.SplitN(strings.SplitN(value, ";", 2)[0], " ", 2)
		if slices.Contains(callbackNetworks, Network(first[0])) {
			if !ok || string(selected.Network) != first[0] {
				t.Errorf("%q must select its first entry on this platform, got %+v", value, selected)
			}
		} else if !ok || selected.Network != NetworkTCP {
			t.Errorf("%q must fall back to the loopback TCP entry on this platform, got %+v", value, selected)
		}
	}
}

// TestDeadlinesFollowTheVectors 回放 deadlines.json（invocation.md §2）：读法与生效的截止时间经 FromMetadata，写出经 ToMetadata；
// Go 的时钟是纳秒，比较不用截断。
func TestDeadlinesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.Deadlines()
	if err != nil {
		t.Fatal(err)
	}
	parseUTC := func(text string) time.Time {
		t.Helper()
		value, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	read := func(parent context.Context, text string) (time.Time, bool) {
		return FromMetadata(parent, &pluginv1.InvocationMetadata{Deadline: text}).Deadline()
	}
	for _, c := range vectors.Valid {
		want := parseUTC(c.UTC)
		// 零值的 time.Time 在 invocation.Context 里表示没有截止时间，最早的那一刻读成晚 1 纳秒（FromMetadata 的说明）
		if want.IsZero() {
			want = want.Add(time.Nanosecond)
		}
		if got, ok := read(context.Background(), c.Text); !ok || !got.Equal(want) {
			t.Errorf("%q read as %v (ok=%v), want %s", c.Text, got, ok, c.UTC)
		}
	}
	for _, text := range vectors.Invalid {
		if got, ok := read(context.Background(), text); ok {
			t.Errorf("%q must be read as no deadline, got %v", text, got)
		}
	}
	for _, c := range vectors.Effective {
		parent := context.Background()
		if c.GRPC != nil {
			var cancel context.CancelFunc
			parent, cancel = context.WithDeadline(parent, parseUTC(*c.GRPC))
			defer cancel()
		}
		got, ok := read(parent, c.Metadata)
		switch {
		case c.Expected == nil && ok:
			t.Errorf("metadata %q with gRPC %v must have no deadline, got %v", c.Metadata, c.GRPC, got)
		case c.Expected != nil && (!ok || !got.Equal(parseUTC(*c.Expected))):
			t.Errorf("metadata %q with gRPC %v gave %v (ok=%v), want %s", c.Metadata, c.GRPC, got, ok, *c.Expected)
		}
	}
	for _, c := range vectors.Written {
		ctx := FromMetadata(context.Background(), &pluginv1.InvocationMetadata{Deadline: c.UTC})
		if written := ToMetadata(ctx).Deadline; written != c.Text {
			t.Errorf("%s written as %q, want %q", c.UTC, written, c.Text)
		}
	}
}

// placeholder 是模板里的占位符（不含格式说明）。
var placeholder = regexp.MustCompile(`\{([A-Za-z0-9_]+)(:[^}]*)?\}`)

// placeholdersOf 返回模板里的占位符名，排好序。
func placeholdersOf(template string) []string {
	var names []string
	for _, match := range placeholder.FindAllStringSubmatch(template, -1) {
		if !slices.Contains(names, match[1]) {
			names = append(names, match[1])
		}
	}
	slices.Sort(names)
	return names
}

// TestRuntimeMessagesFollowTheVectors 回放 plugin-messages.json（invocation.md §8）：SDK 的键、错误码、参数与 en-US 模板与向量一致，
// 自带的 10 种区域每种都有这些键、占位符与 en-US 相同；固定的 reason 文本与向量相同；SDK 不自造别的运行时键。
func TestRuntimeMessagesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.PluginMessages()
	if err != nil {
		t.Fatal(err)
	}
	constructed := map[string]*primitives.Error{
		MessageUnavailable:     processUnavailable(probeID, primitives.Configured),
		MessageUnexpected:      processFailed(probeID, phaseConfigure, reasonConfigurationNotObject),
		MessageUnsubscribed:    eventUnsubscribed(probeID, Envelope{Topic: "t", Type: "x"}, ""),
		MessageHostUnavailable: hostUnavailable(probeID, reasonNoCallbackEndpoint),
		MessageCallbackDenied:  callbackDenied(probeID, "capability:kernel.pipeline"),
		MessageTenantDenied:    tenantDenied(probeID, "t1"),
		MessageSessionInvalid:  (&Host{pluginID: probeID}).failure(invocation.Background(), "invoke", status.Error(codes.Unauthenticated, "x"), "orders.create"),
	}
	english := runtimeCatalog.Locales["en-US"]
	keys := make([]string, 0, len(vectors.Messages))
	for _, message := range vectors.Messages {
		keys = append(keys, message.Key)
		err, ok := constructed[message.Key]
		if !ok {
			t.Errorf("the SDK does not construct %s", message.Key)
			continue
		}
		arguments := slices.Sorted(maps.Keys(err.Details))
		expected := slices.Sorted(slices.Values(message.Arguments))
		if err.Code != message.Code || err.MessageKey != message.Key || !slices.Equal(arguments, expected) {
			t.Errorf("%s: the SDK writes %s with %v, want %s with %v", message.Key, err.Code, arguments, message.Code, expected)
		}
		if english[message.Key] != message.Template {
			t.Errorf("%s: the en-US template %q, want %q", message.Key, english[message.Key], message.Template)
		}
		if names := placeholdersOf(message.Template); !slices.Equal(names, expected) {
			t.Errorf("%s: the template's placeholders %v differ from its arguments %v", message.Key, names, expected)
		}
		if template, _ := primitives.DefaultTemplate(message.Key); template != message.Template {
			t.Errorf("%s: the default template %q", message.Key, template)
		}
	}
	slices.Sort(keys)
	for _, locale := range Locales {
		templates, ok := runtimeCatalog.Locales[locale]
		if !ok {
			t.Errorf("the runtime catalog has no %s", locale)
			continue
		}
		if got := slices.Sorted(maps.Keys(templates)); !slices.Equal(got, keys) {
			t.Errorf("%s has the keys %v, want %v", locale, got, keys)
		}
		for key, template := range templates {
			if !slices.Equal(placeholdersOf(template), placeholdersOf(english[key])) {
				t.Errorf("%s %s: placeholders %v differ from en-US %v", locale, key, placeholdersOf(template), placeholdersOf(english[key]))
			}
		}
	}
	// 内嵌的目录就是这 10 个文件，没有别的
	entries, err := fs.ReadDir(runtimeLocalization, "Localization")
	if err != nil || len(entries) != len(Locales) {
		t.Errorf("the embedded catalog has %d files (%v), want %d", len(entries), err, len(Locales))
	}

	// 读入错误的两条 reason 由 FromProtoError 与 FromStatus（rules.ReadProtoError、ReadProtoStatus）写出
	invalidCode := FromProtoError(&pluginv1.Error{Code: "x"}, probeID).Details["reason"]
	withoutError := FromStatus(&pluginv1.Status{}, probeID, "publish").Err().Details["reason"]
	known := []any{reasonConfigurationNotObject, invalidCode, withoutError, reasonResultCount(1, 2), reasonNoCallbackEndpoint}
	for _, reason := range vectors.Reasons {
		switch reason.Reason {
		case "the plugin reported a failure without an error", "{status}: {message}", "{type}":
			// 宿主读插件的那一条不在 SDK 里；后两条由 rules.StatusReason 与 errorTypeName 写出，见别的测试
		default:
			text := strings.NewReplacer("{code}", "x", "{results}", "1", "{envelopes}", "2").Replace(reason.Reason)
			if !slices.Contains(known, any(text)) {
				t.Errorf("the vector's reason %q has no counterpart in the SDK", reason.Reason)
			}
		}
	}
	if reasonPanic != "panic" {
		t.Errorf("reason %q", reasonPanic)
	}
	// RuntimeCatalog 是副本：改它不影响运行时自己的目录
	copied := RuntimeCatalog()
	copied.Locales["en-US"][MessageUnavailable] = "changed"
	if copied.Name != RuntimeCatalogName || runtimeCatalog.Locales["en-US"][MessageUnavailable] == "changed" {
		t.Error("RuntimeCatalog must return a copy")
	}
}

// TestErrorReadingFollowsTheVectors 回放 error-reading.json（invocation.md §3）：cases 经 FromProtoError 读，statusWithoutError 里插件读宿主的
// 用例经 FromStatus 读（ok 为假却没有 error 与整个 status 缺失两种写法）；宿主读插件的用例不在 SDK 里。
func TestErrorReadingFollowsTheVectors(t *testing.T) {
	vectors, err := conformance.ErrorReading()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Cases {
		compareRead(t, c.Name, FromProtoError(protoErrorOf(c.Error), vectors.PluginID), &c.Expected)
	}
	read := 0
	for _, c := range vectors.StatusWithoutError {
		if c.Reader != "plugin" {
			continue
		}
		read++
		operation := ""
		if c.Operation != nil {
			operation = *c.Operation
		}
		// Host.Invoke 的 phase 是操作 ID，Host.Publish 是 publish：与 Host 里取 phase 的写法相同
		phase := rules.CallbackPhase(c.RPC, operation)
		compareRead(t, "status without an error "+phase, FromStatus(&pluginv1.Status{Ok: false}, vectors.PluginID, phase).Err(), &c.Expected)
		compareRead(t, "missing status "+phase, FromStatus(nil, vectors.PluginID, phase).Err(), &c.Expected)
	}
	if read == 0 {
		t.Error("the vectors must have cases of the plugin reading the host")
	}
}

// protoErrorOf 把向量里的线上 Error 换成协议的 Error。
func protoErrorOf(wire conformance.ProtoError) *pluginv1.Error {
	converted := &pluginv1.Error{Code: wire.Code, MessageKey: wire.MessageKey, Text: wire.Text, Details: wire.Details}
	if wire.Inner != nil {
		converted.Inner = protoErrorOf(*wire.Inner)
	}
	return converted
}

// compareRead 按 JSON 值比较读入的错误与期望：键为 null 即没有键（自由文本错误），文案为 null 即带键的错误没有文案，细节的数字按 JSON 数值比较。
func compareRead(t *testing.T, name string, got *primitives.Error, want *conformance.DecodedErrorExpectation) {
	t.Helper()
	if want == nil || got == nil {
		if (want == nil) != (got == nil) {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
		return
	}
	key, text := "", ""
	if want.Key != nil {
		key = *want.Key
	}
	if want.Text != nil {
		text = *want.Text
	}
	gotText := got.Text
	if got.MessageKey == "" {
		gotText = got.Message()
	}
	if got.Code != want.Code || got.MessageKey != key || gotText != text {
		t.Errorf("%s: got %s %q %q, want %s %q %q", name, got.Code, got.MessageKey, gotText, want.Code, key, text)
	}
	var expected any
	if err := json.Unmarshal(want.Details, &expected); err != nil {
		t.Fatal(err)
	}
	var actual any
	if got.Details != nil {
		encoded, err := json.Marshal(got.Details)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &actual); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("%s: details %v, want %s", name, actual, want.Details)
	}
	compareRead(t, name+" inner", got.Inner, want.Inner)
}
