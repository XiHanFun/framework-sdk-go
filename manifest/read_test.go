package manifest_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/manifest"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// readError 读取清单并返回错误的码、键与参数
func readError(t *testing.T, data []byte) (string, string, map[string]any) {
	t.Helper()
	_, err := manifest.Read(data)
	var failure *primitives.Error
	if !errors.As(err, &failure) {
		t.Fatalf("%s: expected *primitives.Error, got %v", data, err)
	}
	return failure.Code, failure.MessageKey, failure.Details
}

// TestReadReportsPathsInTextOrder 核对读取的检查顺序与错误（RFC-0002 解析规则 2，ADR-0236）：JSON 文本 → 字面量 null → 形状 → 跨字段约束，
// 形状按文本顺序报第一处的路径，对象查完后按 required 的顺序报缺少的成员
func TestReadReportsPathsInTextOrder(t *testing.T) {
	const base = `"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable"`
	cases := []struct{ data, key, argument, value string }{
		{"\xef\xbb\xbf{}", manifest.MessageInvalidJSON, "reason", "$"},
		{``, manifest.MessageInvalidJSON, "reason", "$"},
		{`{"id":"a","id":"b",}`, manifest.MessageInvalidJSON, "reason", "$"},
		{`{"provides":[{"id":"a.b","id":"c.d"}]}`, manifest.MessageInvalidJSON, "reason", "$.provides[0].id"},
		{`null`, manifest.MessageEmpty, "", ""},
		{`5`, manifest.MessageInvalid, "reason", "$"},
		{`{}`, manifest.MessageInvalid, "reason", "$.id"},
		{`{"x":null}`, manifest.MessageInvalid, "reason", "$.x"},
		{`{"id":null}`, manifest.MessageInvalid, "reason", "$.id"},
		{`{"kind":"library","id":"bad"}`, manifest.MessageInvalid, "reason", "$.kind"},
		{`{"id":"bad","kind":"library"}`, manifest.MessageInvalid, "reason", "$.id"},
		{`{"id":"xihan.a.b","version":"1.0.0","primitives":"2147483648","stability":"stable","kind":"capability"}`, manifest.MessageInvalid, "reason", "$.primitives"},
		{`{` + base + `,"kind":"capability","provides":[{"version":"x","id":"a.b"}]}`, manifest.MessageInvalid, "reason", "$.provides[0].version"},
		{`{` + base + `,"kind":"capability","provides":[{"id":"a.b"}]}`, manifest.MessageInvalid, "reason", "$.provides[0].version"},
		{`{` + base + `,"kind":"capability","requires":[{"id":"a.b","version":"^2147483648"}]}`, manifest.MessageInvalid, "reason", "$.requires[0].version"},
		{`{` + base + `,"kind":"capability","contributes":{"pipeline":[{"middleware":"","stage":"observe"}]}}`, manifest.MessageInvalid, "reason", "$.contributes.pipeline[0].middleware"},
		{`{` + base + `,"kind":"capability","contributes":{"pipeline":[{"middleware":"a","stage":"Observe"}]}}`, manifest.MessageInvalid, "reason", "$.contributes.pipeline[0].stage"},
		{`{` + base + `,"kind":"capability","contributes":{"health":["Store"]}}`, manifest.MessageInvalid, "reason", "$.contributes.health[0]"},
		{`{` + base + `,"kind":"capability","contributes":{"permissions":["a.b","c.d","a.b"]}}`, manifest.MessageInvalid, "reason", "$.contributes.permissions[2]"},
		{`{` + base + `,"kind":"capability","contributes":{"config":{"section":"XiHan:Orders","schema":{"type":"object"}}}}`, manifest.MessageInvalid, "reason", "$.contributes.config.schema"},
		{`{` + base + `,"kind":"plugin","provides":[{"id":"a.b","version":"1"},{"id":"a.b","version":"2"}]}`, manifest.MessageIsolationRequired, "id", "xihan.a.b"},
		{`{` + base + `,"kind":"capability","isolation":"wasm"}`, manifest.MessageIsolationNotAllowed, "id", "xihan.a.b"},
		{`{` + base + `,"kind":"capability","provides":[{"id":"a.b","version":"1"},{"id":"a.b","version":"2"}]}`, manifest.MessageDuplicateProvides, "id", "xihan.a.b"},
		{`{` + base + `,"kind":"capability","requires":[{"id":"a.b","version":"1"},{"id":"a.b","version":"^1"}]}`, manifest.MessageDuplicateRequires, "id", "xihan.a.b"},
		{`{` + base + `,"kind":"capability","bindings":[{"name":"db","type":"queue"},{"name":"db","type":"queue"}]}`, manifest.MessageDuplicateBindings, "id", "xihan.a.b"},
	}
	for _, c := range cases {
		code, key, details := readError(t, []byte(c.data))
		if code != manifest.ErrorCodeInvalidManifest || key != c.key || (c.argument == "" && len(details) != 0) || (c.argument != "" && (len(details) != 1 || details[c.argument] != c.value)) {
			t.Errorf("%s: %s %s %v, want %s {%s: %s}", c.data, code, key, details, c.key, c.argument, c.value)
		}
	}
	failure := primitives.NewError(manifest.ErrorCodeInvalidManifest, manifest.MessageInvalid, map[string]any{"reason": "$.kind"}, nil)
	if failure.Message() != "The manifest is not valid: $.kind" {
		t.Errorf("message %q", failure.Message())
	}
}

// TestReadFileSkipsTheBOMAndReportsMissingFiles 核对 ReadFile 按 UTF-8 读、跳过开头的 UTF-8 BOM，文件不存在为 kernel.manifest.file_missing
func TestReadFileSkipsTheBOMAndReportsMissingFiles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, manifest.FileName)
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, echoManifest...), 0o600); err != nil {
		t.Fatal(err)
	}
	if m, err := manifest.ReadFile(path); err != nil || m.ID != "xihan.samples.echo" {
		t.Errorf("read %v (%v)", m, err)
	}
	for _, missing := range []string{filepath.Join(directory, "absent.json"), directory} {
		_, err := manifest.ReadFile(missing)
		var failure *primitives.Error
		if !errors.As(err, &failure) || failure.Code != manifest.ErrorCodeInvalidManifest || failure.MessageKey != manifest.MessageFileMissing || failure.Details["path"] != missing {
			t.Errorf("%s: %v", missing, err)
		}
	}
}

// TestWriteFollowsTheFixedLayout 核对写出的排版（RFC-0002「写出」，ADR-0236）：两个空格缩进、LF、末尾不换行、成员顺序固定、列表总写，
// 缺省的 role 与 activation 写出，字符串只转义必须转义的字符；写出再读得到同一份清单
func TestWriteFollowsTheFixedLayout(t *testing.T) {
	optional := false
	m := &manifest.Manifest{
		ID: "xihan.orders.core", Package: "Échø <&>", Version: "1.0.0", Primitives: 2, Stability: primitives.Preview, Kind: manifest.KindCapability,
		Requires:    []manifest.CapabilityRequirement{{ID: "kernel.hosting", Version: 1}},
		Bindings:    []manifest.Binding{{Name: "db", Type: "connection:relational", Required: &optional}},
		Contributes: &manifest.Contributions{Pipeline: []manifest.PipelineContribution{{Middleware: "audit", Stage: primitives.Observe, After: []string{"x"}}}},
		Activation:  []string{},
	}
	data, err := manifest.Write(m)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"{", `  "$schema": "https://spec.xihanfun.com/manifest/v1.json",`, `  "id": "xihan.orders.core",`, `  "package": "Échø <&>",`,
		`  "version": "1.0.0",`, `  "primitives": "2",`, `  "stability": "preview",`, `  "kind": "capability",`, `  "role": "provider",`,
		`  "provides": [],`, `  "requires": [`, "    {", `      "id": "kernel.hosting",`, `      "version": "1",`, `      "optional": false`, "    }", "  ],",
		`  "bindings": [`, "    {", `      "name": "db",`, `      "type": "connection:relational",`, `      "required": false`, "    }", "  ],",
		`  "contributes": {`, `    "pipeline": [`, "      {", `        "middleware": "audit",`, `        "stage": "observe",`, `        "before": [],`,
		`        "after": [`, `          "x"`, "        ]", "      }", "    ],", `    "operations": [],`, `    "permissions": [],`, `    "health": []`, "  },",
		`  "activation": [`, `    "onStartup"`, "  ],", `  "permissions": []`, "}",
	}, "\n")
	if string(data) != want {
		t.Errorf("written:\n%s\nwant:\n%s", data, want)
	}
	again, err := manifest.Read(data)
	if err != nil || again.Package != m.Package || again.Bindings[0].IsRequired() || again.Contributes.Pipeline[0].After[0] != "x" {
		t.Errorf("read again %+v (%v)", again, err)
	}
}

// TestValidateChecksTheWrittenForm 核对 Validate：先按 Schema 核对写出的形态（PRIMITIVES_VALIDATION_001 + kernel.manifest.invalid），
// 再按跨字段约束的顺序报 primitives.manifest.* 的键
func TestValidateChecksTheWrittenForm(t *testing.T) {
	base := func() *manifest.Manifest {
		return &manifest.Manifest{ID: "xihan.a.b", Version: "1.0.0", Primitives: 1, Kind: manifest.KindCapability}
	}
	if err := base().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		change func(*manifest.Manifest)
		key    string
		detail string
	}{
		{func(m *manifest.Manifest) { m.ID = "a.b" }, manifest.MessageInvalid, "$.id"},
		{func(m *manifest.Manifest) { m.Primitives = 0 }, manifest.MessageInvalid, "$.primitives"},
		{func(m *manifest.Manifest) { m.Kind = "" }, manifest.MessageInvalid, "$.kind"},
		{func(m *manifest.Manifest) {
			m.Kind = manifest.KindPlugin
			m.Provides = []manifest.CapabilityDeclaration{{ID: "a.b", Version: 1}, {ID: "a.b", Version: 1}}
		}, manifest.MessageIsolationRequired, "xihan.a.b"},
		{func(m *manifest.Manifest) { m.Permissions = []string{"capability:x"} }, manifest.MessageIsolationNotAllowed, "xihan.a.b"},
		{func(m *manifest.Manifest) {
			m.Provides = []manifest.CapabilityDeclaration{{ID: "a.b", Version: 1}, {ID: "a.b", Version: 2}}
		}, manifest.MessageDuplicateProvides, "xihan.a.b"},
	}
	for _, c := range cases {
		m := base()
		c.change(m)
		var failure *primitives.Error
		if err := m.Validate(); !errors.As(err, &failure) || failure.Code != primitives.ErrorCodeInvalidArgument || failure.MessageKey != c.key {
			t.Errorf("%s: %v", c.key, err)
			continue
		}
		for _, value := range failure.Details {
			if value != c.detail {
				t.Errorf("%s: details %v, want %s", c.key, failure.Details, c.detail)
			}
		}
	}
}
