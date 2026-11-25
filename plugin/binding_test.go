package plugin

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
)

// 测试用的主值与属性值：够长、够特别，在输出里搜得出来。
const (
	secretValue    = "postgres://main-value-0f3a/app"
	secretPassword = "property-value-7c1e"
	secretToken    = "property-value-9b2d"
)

// secretBinding 是带主值与两个属性的绑定；属性名只差大小写的各算一个。
func secretBinding() Binding {
	return Binding{Name: "db", Type: "connection:relational", Value: secretValue, Properties: map[string]string{"password": secretPassword, "Password": secretToken, "empty": ""}}
}

// expectNoSecrets 核对文本不带主值与属性值，原文与十六进制写法都不带。
func expectNoSecrets(t *testing.T, what, text string) {
	t.Helper()
	for _, secret := range []string{secretValue, secretPassword, secretToken} {
		encoded := hex.EncodeToString([]byte(secret))
		if strings.Contains(text, secret) || strings.Contains(strings.ToLower(text), encoded) {
			t.Errorf("%s must not carry the value or a property value of a binding, got %.300q", what, text)
		}
	}
}

// TestBindingsAreHandedOverWithTheirProperties 核对 Configure 下发的绑定怎样交给作者（lifecycle.md §7.2，ADR-0285）：保持先后；
// 属性名不折叠大小写、不改写，像数字的、带点的名字照旧；值为空的属性在表里；没有属性（字段没发或发了空表）是空表、不是 nil；
// 主值为空不是错误；交出的是拷贝，两边互不影响。
func TestBindingsAreHandedOverWithTheirProperties(t *testing.T) {
	if got := bindingsOf(nil); len(got) != 0 {
		t.Errorf("no bindings must give no bindings, got %d", len(got))
	}
	delivered := []*pluginv1.Binding{
		{Name: "db", Type: "connection:relational", Properties: map[string]string{"accessKey": "a", "AccessKey": "b", "0": "zero", "8080": "port", "tls.crt": "", "__proto__": "p"}},
		{Name: "cache", Type: "cache:distributed", Value: "redis.internal:6379"},
		{Name: "queue", Type: "queue", Value: "amqp://mq.internal", Properties: map[string]string{}},
	}
	got := bindingsOf(delivered)
	want := []Binding{
		{Name: "db", Type: "connection:relational", Properties: map[string]string{"accessKey": "a", "AccessKey": "b", "0": "zero", "8080": "port", "tls.crt": "", "__proto__": "p"}},
		{Name: "cache", Type: "cache:distributed", Value: "redis.internal:6379", Properties: map[string]string{}},
		{Name: "queue", Type: "queue", Value: "amqp://mq.internal", Properties: map[string]string{}},
	}
	// reflect.DeepEqual 区分 nil 与空表：没有属性的两个都必须是空表
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the bindings handed over differ: got %d bindings named %v", len(got), names(got))
	}

	// 拷贝：作者改自己的表碰不到请求消息，请求消息之后的变化也到不了作者手里
	got[0].Properties["accessKey"] = "changed"
	got[0].Properties["added"] = "x"
	delete(got[0].Properties, "0")
	if properties := delivered[0].Properties; properties["accessKey"] != "a" || properties["0"] != "zero" || len(properties) != 6 {
		t.Error("changing the author's properties must not change the request")
	}
	delivered[1].Properties = map[string]string{"late": "x"}
	delivered[2].Properties["late"] = "x"
	if len(got[1].Properties) != 0 || len(got[2].Properties) != 0 {
		t.Error("changing the request afterwards must not change the author's properties")
	}
}

// names 返回绑定名，失败信息只列名字。
func names(bindings []Binding) []string {
	result := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		result = append(result, binding.Name)
	}
	return result
}

// TestBindingDebugOutputCarriesNoValues 核对绑定的调试输出（lifecycle.md §7.2）：fmt 的每个动词，直接给、给指针、放在切片与
// Configuration 里，都只带绑定名、类型与按升序排列的属性名，不带主值与属性值。
func TestBindingDebugOutputCarriesNoValues(t *testing.T) {
	binding := secretBinding()
	const text = `Binding{Name: "db", Type: "connection:relational", Properties: ["Password" "empty" "password"]}`
	if got := binding.String(); got != text {
		t.Errorf("String() gave %q, want %q", got, text)
	}
	if got := binding.GoString(); got != "plugin."+text {
		t.Errorf("GoString() gave %q, want %q", got, "plugin."+text)
	}
	exact := map[string]string{
		"%v":   text,
		"%+v":  text,
		"%s":   text,
		"%#v":  "plugin." + text,
		"%q":   fmt.Sprintf("%q", text),
		"%x":   hex.EncodeToString([]byte(text)),
		"%d":   "%!d(plugin.Binding=" + text + ")",
		"%.7v": "Binding",
		"%99v": strings.Repeat(" ", 99-len(text)) + text,
	}
	for format, want := range exact {
		if got := fmt.Sprintf(format, binding); got != want {
			t.Errorf("%s gave %q, want %q", format, got, want)
		}
		if got := fmt.Sprintf(format, &binding); got != want {
			t.Errorf("%s of a pointer gave %q, want %q", format, got, want)
		}
	}

	// 没有属性的绑定：自己构造的零值是 nil 表，输出与空表相同
	if got := fmt.Sprint(Binding{Name: "cache", Type: "cache:distributed", Value: secretValue}); got != `Binding{Name: "cache", Type: "cache:distributed", Properties: []}` {
		t.Errorf("a binding without properties printed as %q", got)
	}

	configuration := Configuration{Section: json.RawMessage(`{}`), Bindings: []Binding{binding, {Name: "cache", Value: secretValue}}}
	holder := struct{ Held Binding }{binding}
	for _, value := range []any{binding, &binding, []Binding{binding}, []*Binding{&binding}, map[string]Binding{"db": binding}, configuration, &configuration, holder} {
		// %p 不在内：fmt 先于任何方法处理它，用在不是指针的值上时按字段打印，类型拦不住（go vet 会报这种写法）
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%t", "%T"} {
			printed := fmt.Sprintf(format, value)
			expectNoSecrets(t, fmt.Sprintf("%s of %T", format, value), printed)
			// %v 一族看得出是哪个绑定、有哪些属性
			if (format == "%v" || format == "%+v" || format == "%#v") && !(strings.Contains(printed, `Name: "db"`) && strings.Contains(printed, `["Password" "empty" "password"]`)) {
				t.Errorf("%s of %T must carry the name and the property names, got %.300q", format, value, printed)
			}
		}
	}
	expectNoSecrets(t, "Sprint", fmt.Sprint(binding, []Binding{binding}))
	expectNoSecrets(t, "Sprintln", fmt.Sprintln(configuration))
	expectNoSecrets(t, "an error wrapping a binding", fmt.Errorf("configure %v: %w", binding, errors.New("failed")).Error())
}

// TestBindingLogValueCarriesNoValues 核对 slog 里的绑定（lifecycle.md §7.2）：运行时的 JSON 行处理器与文本处理器都只写
// name、type 与 propertyNames（属性名的数组），不带主值与属性值；指针同样。
func TestBindingLogValueCarriesNoValues(t *testing.T) {
	binding := secretBinding()
	var lines bytes.Buffer
	logger := newTestLogger(&lines)
	logger.Info("configured", "binding", binding, "pointer", &binding, slog.Group("group", slog.Any("binding", binding)))
	expectNoSecrets(t, "the JSON log line", lines.String())
	var line struct {
		Binding json.RawMessage `json:"binding"`
		Pointer json.RawMessage `json:"pointer"`
		Group   struct {
			Binding json.RawMessage `json:"binding"`
		} `json:"group"`
	}
	if err := json.Unmarshal(lines.Bytes(), &line); err != nil {
		t.Fatalf("the log line must be JSON: %v", err)
	}
	const logged = `{"name":"db","type":"connection:relational","propertyNames":["Password","empty","password"]}`
	for what, got := range map[string]json.RawMessage{"binding": line.Binding, "pointer": line.Pointer, "group.binding": line.Group.Binding} {
		if string(got) != logged {
			t.Errorf("%s logged as %s, want %s", what, got, logged)
		}
	}
	// 没有属性：propertyNames 是空数组、不是 null
	lines.Reset()
	logger.Info("configured", "binding", Binding{Name: "cache", Type: "cache:distributed", Value: secretValue})
	if !strings.Contains(lines.String(), `"binding":{"name":"cache","type":"cache:distributed","propertyNames":[]}`) {
		t.Errorf("a binding without properties logged as %.300q", lines.String())
	}
	expectNoSecrets(t, "the JSON log line", lines.String())

	// 文本处理器：单个绑定经 LogValue 展开成组，切片与 Configuration 经 fmt 的 %+v
	var text bytes.Buffer
	plain := slog.New(slog.NewTextHandler(&text, nil))
	plain.Info("configured", "binding", binding, "bindings", []Binding{binding}, "configuration", Configuration{Bindings: []Binding{binding}})
	expectNoSecrets(t, "the text log line", text.String())
	for _, part := range []string{"binding.name=db", "binding.type=connection:relational", `binding.propertyNames="[Password empty password]"`} {
		if !strings.Contains(text.String(), part) {
			t.Errorf("the text log line must contain %s, got %.300q", part, text.String())
		}
	}
}

// TestConfigureKeepsBindingValuesOutOfTheLogAndTheStatus 核对运行时自己的输出（lifecycle.md §7.2）：配置回调成功、返回错误、panic 三种情况下，
// 运行时写的日志与回给宿主的 Status 都不带绑定的主值与属性值，作者照常拿到它们。
func TestConfigureKeepsBindingValuesOutOfTheLogAndTheStatus(t *testing.T) {
	hooks := map[string]func() error{
		"succeeds": func() error { return nil },
		"fails":    func() error { return errors.New("the connection could not be opened") },
		"panics":   func() error { panic("the connection could not be opened") },
	}
	for name, outcome := range hooks {
		t.Run(name, func(t *testing.T) {
			var received []Binding
			h := launch(t, defaultEnv(), func(opts *Options, seen *recorder) {
				opts.Hooks.Configure = func(_ context.Context, configuration Configuration) error {
					seen.record(func() { received = configuration.Bindings })
					return outcome()
				}
			})
			ctx := deadline(t)
			h.handshake(ctx)
			configured, err := h.client.Configure(ctx, &pluginv1.ConfigureRequest{
				Configuration:      `{}`,
				GrantedPermissions: allPermissions,
				Bindings:           []*pluginv1.Binding{{Name: "db", Type: "connection:relational", Value: secretValue, Properties: map[string]string{"password": secretPassword, "Password": secretToken}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if ok := configured.GetStatus().GetOk(); ok != (name == "succeeds") {
				t.Errorf("configure answered ok=%v", ok)
			}
			got := read(h.seen, func() []Binding { return received })
			if len(got) != 1 || got[0].Value != secretValue || got[0].Properties["password"] != secretPassword || got[0].Properties["Password"] != secretToken {
				t.Errorf("the author must receive the value and the properties, got %v", got)
			}
			expectNoSecrets(t, "the runtime log", h.stderr.String())
			expectNoSecrets(t, "the status", protojson.Format(configured))
		})
	}
}

// TestBindingJSONCarriesNoValues 核对 encoding/json 写出的绑定（lifecycle.md §7.2）：单个绑定、指针、切片、映射、
// 嵌在结构里与整个 Configuration 都只写 {"name","type","propertyNames"}，属性名按升序、没有属性时是空数组，
// 不带主值与属性值；成员、先后与写法同 TypeScript SDK 的 toJSON。Unmarshal 读不回主值与属性值。
func TestBindingJSONCarriesNoValues(t *testing.T) {
	binding := secretBinding()
	const text = `{"name":"db","type":"connection:relational","propertyNames":["Password","empty","password"]}`
	const bare = `{"name":"cache","type":"cache:distributed","propertyNames":[]}`
	cache := Binding{Name: "cache", Type: "cache:distributed", Value: secretValue}
	configuration := Configuration{Section: json.RawMessage(`{"pool":4}`), GrantedPermissions: []string{"binding:db"}, Bindings: []Binding{binding, cache}, WorkDirectory: "/work"}
	configured := `{"Section":{"pool":4},"GrantedPermissions":["binding:db"],"Bindings":[` + text + "," + bare + `],"WorkDirectory":"/work"}`
	cases := []struct {
		what  string
		value any
		want  string
	}{
		{"a binding", binding, text},
		{"a pointer", &binding, text},
		{"a slice", []Binding{binding, cache}, "[" + text + "," + bare + "]"},
		{"a slice of pointers", []*Binding{&binding}, "[" + text + "]"},
		{"a map", map[string]Binding{"db": binding}, `{"db":` + text + `}`},
		{"a struct field", struct{ Held Binding }{binding}, `{"Held":` + text + `}`},
		{"a nil table", cache, bare},
		{"an empty table", Binding{Name: "cache", Type: "cache:distributed", Value: secretValue, Properties: map[string]string{}}, bare},
		{"a configuration", configuration, configured},
		{"a configuration pointer", &configuration, configured},
	}
	for _, c := range cases {
		written, err := json.Marshal(c.value)
		if err != nil {
			t.Fatalf("Marshal of %s failed: %v", c.what, err)
		}
		if got := string(written); got != c.want {
			t.Errorf("Marshal of %s gave %s, want %s", c.what, got, c.want)
		}
		expectNoSecrets(t, "Marshal of "+c.what, string(written))
	}

	// 缩进与不转义 HTML 的编码器同样经过 MarshalJSON
	indented, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	expectNoSecrets(t, "MarshalIndent of a Configuration", string(indented))
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode([]Binding{binding}); err != nil {
		t.Fatal(err)
	}
	if got := encoded.String(); got != "["+text+"]\n" {
		t.Errorf("Encoder gave %q", got)
	}

	// 与 TypeScript SDK 的 toJSON 同样的文本：紧凑写法，成员依次是 name、type、propertyNames，属性名按升序
	const typescript = `{"name":"db","type":"connection:relational","propertyNames":["password","username"]}`
	shared := Binding{Name: "db", Type: "connection:relational", Value: secretValue, Properties: map[string]string{"username": "app", "password": secretPassword}}
	if written, err := json.Marshal(shared); err != nil || string(written) != typescript {
		t.Errorf("Marshal gave %s (%v), want the text the TypeScript SDK writes: %s", written, err, typescript)
	}

	// 遮过的文本读不回主值与属性值：没有 UnmarshalJSON，按字段读到的只有名字与类型
	var back Binding
	if err := json.Unmarshal([]byte(text), &back); err != nil {
		t.Fatal(err)
	}
	if back.Name != "db" || back.Type != "connection:relational" || back.Value != "" || back.Properties != nil {
		t.Errorf("Unmarshal of the masked text gave %v with value %q and %d properties", back, back.Value, len(back.Properties))
	}
}

// TestLogHandlerWritesBindingsWithoutValues 核对运行时的 JSON 行处理器（NewLogHandler）记绑定的切片与整个 Configuration：
// 它们不经 LogValue，由 encoding/json 写出，每个绑定同样只有名字、类型与属性名（lifecycle.md §7.2）。
func TestLogHandlerWritesBindingsWithoutValues(t *testing.T) {
	binding := secretBinding()
	configuration := Configuration{Section: json.RawMessage(`{}`), Bindings: []Binding{binding}}
	var lines bytes.Buffer
	logger := newTestLogger(&lines)
	logger.Info("configured", "bindings", []Binding{binding}, "pointers", []*Binding{&binding}, "configuration", configuration, "pointer", &configuration)
	expectNoSecrets(t, "the JSON log line", lines.String())
	var line struct {
		Bindings      json.RawMessage `json:"bindings"`
		Pointers      json.RawMessage `json:"pointers"`
		Configuration struct {
			Bindings json.RawMessage `json:"Bindings"`
		} `json:"configuration"`
		Pointer struct {
			Bindings json.RawMessage `json:"Bindings"`
		} `json:"pointer"`
	}
	if err := json.Unmarshal(lines.Bytes(), &line); err != nil {
		t.Fatalf("the log line must be JSON: %v", err)
	}
	const logged = `[{"name":"db","type":"connection:relational","propertyNames":["Password","empty","password"]}]`
	for what, got := range map[string]json.RawMessage{"bindings": line.Bindings, "pointers": line.Pointers, "configuration.Bindings": line.Configuration.Bindings, "pointer.Bindings": line.Pointer.Bindings} {
		if string(got) != logged {
			t.Errorf("%s logged as %s, want %s", what, got, logged)
		}
	}
}
