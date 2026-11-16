package manifest_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/manifest"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

const echoManifest = `{
  "$schema": "https://spec.xihanfun.com/manifest/v1.json",
  "id": "xihan.samples.echo",
  "package": "EchoPlugin",
  "version": "5.0.0-preview.1",
  "primitives": "1",
  "stability": "experimental",
  "kind": "plugin",
  "isolation": "out-of-process",
  "requires": [ { "id": "kernel.plugins", "version": "1" } ],
  "contributes": {
    "operations": [ "echoes.create", "echoes.relay", "echoes.crash", "events.list" ],
    "config": { "section": "XiHan:Echo" },
    "health": [ "echo" ]
  },
  "permissions": [ "capability:kernel.pipeline" ],
  "activation": [ "onStartup" ],
  "entry": "EchoPlugin"
}`

func TestReadParsesAPluginManifest(t *testing.T) {
	m, err := manifest.Read([]byte(echoManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "xihan.samples.echo" || m.Kind != manifest.KindPlugin || m.Isolation != manifest.IsolationOutOfProcess || m.Primitives != 1 || m.Stability != primitives.Experimental {
		t.Errorf("parsed %+v", m)
	}
	if len(m.Requires) != 1 || m.Requires[0].String() != "kernel.plugins@1" {
		t.Errorf("requires %v", m.Requires)
	}
	if len(m.Operations()) != 4 || m.ConfigSection() != "XiHan:Echo" || len(m.Health()) != 1 || !m.ActivatesOnStartup() || m.EffectiveRole() != manifest.RoleProvider {
		t.Error("accessors")
	}
	if manifest.Domain(m.ID) != "samples" || manifest.Domain("caching.distributed") != "caching" {
		t.Error("domain")
	}
	written, err := manifest.Write(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := manifest.Read(written)
	if err != nil || again.ID != m.ID || len(again.Requires) != 1 || again.Requires[0].Version != 1 {
		t.Errorf("round trip: %v %+v", err, again)
	}
	if !strings.Contains(string(written), `"primitives": "1"`) || !strings.Contains(string(written), `"stability": "experimental"`) {
		t.Errorf("written %s", written)
	}
}

// TestConfigSectionMayHaveSubsections 核对配置节文法与宿主一致：领域节后可接任意层 PascalCase 子节（RFC-0002「配置节」）。
func TestConfigSectionMayHaveSubsections(t *testing.T) {
	const template = `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","contributes":{"config":{"section":%q}}}`
	for _, section := range []string{"XiHan:Caching", "XiHan:Caching:Redis", "XiHan:Protocols:CloudEvents", "XiHan:ObjectStorage:SignedUrls", "XiHan:A:B2:C"} {
		m, err := manifest.Read([]byte(fmt.Sprintf(template, section)))
		if err != nil {
			t.Errorf("%q must be accepted: %v", section, err)
			continue
		}
		if m.ConfigSection() != section {
			t.Errorf("%q read as %q", section, m.ConfigSection())
		}
	}
	for _, section := range []string{"Orders", "XiHan", "XiHan:", "XiHan:caching", "XiHan:Caching:", "XiHan:Caching::Redis", "XiHan:Caching:redis", "XiHan:Caching:Re-dis", "xihan:Caching", "XiHan:Caching:Redis:"} {
		if _, err := manifest.Read([]byte(fmt.Sprintf(template, section))); err == nil {
			t.Errorf("%q must be rejected", section)
		}
	}
}

func TestContributedPermissionCodesFollowTheHostGrammar(t *testing.T) {
	const template = `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"plugin","isolation":"out-of-process","contributes":{"permissions":%s}}`
	for _, codes := range []string{`[]`, `["orders.read"]`, `["sales.orders.read","sales.orders.write"]`, `["echo2.items-v2.read"]`} {
		m, err := manifest.Read([]byte(fmt.Sprintf(template, codes)))
		if err != nil {
			t.Errorf("%s must be accepted: %v", codes, err)
			continue
		}
		if m.Contributes == nil {
			t.Errorf("%s read without contributions", codes)
		}
	}
	for _, codes := range []string{`["orders"]`, `["Orders.read"]`, `["orders.Read"]`, `["orders..read"]`, `["orders.read."]`, `[".orders.read"]`, `["1orders.read"]`, `["orders.1read"]`, `["orders_x.read"]`, `["orders:read"]`, `["orders.read","orders.read"]`, `["orders.read","orders.write","orders.read"]`} {
		if _, err := manifest.Read([]byte(fmt.Sprintf(template, codes))); err == nil {
			t.Errorf("%s must be rejected", codes)
		}
	}
}

func TestReadRejectsInvalidManifests(t *testing.T) {
	cases := map[string]string{
		"unknown field":          `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","extra":1}`,
		"bad id":                 `{"id":"a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability"}`,
		"bad version":            `{"id":"xihan.a.b","version":"1.0","primitives":"1","stability":"stable","kind":"capability"}`,
		"bad primitives":         `{"id":"xihan.a.b","version":"1.0.0","primitives":"0","stability":"stable","kind":"capability"}`,
		"bad stability":          `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"solid","kind":"capability"}`,
		"bad kind":               `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"thing"}`,
		"plugin needs isolation": `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"plugin"}`,
		"non-plugin isolation":   `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","isolation":"wasm"}`,
		"non-plugin permissions": `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","permissions":["capability:x"]}`,
		"duplicate provides":     `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","provides":[{"id":"a.b","version":"1"},{"id":"a.b","version":"2"}]}`,
		"duplicate requires":     `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","requires":[{"id":"a.b","version":"1"},{"id":"a.b","version":"^1"}]}`,
		"bad activation":         `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"plugin","isolation":"wasm","activation":["onBoot"]}`,
		"bad permission":         `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"plugin","isolation":"wasm","permissions":["sudo"]}`,
		"bad operation":          `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","contributes":{"operations":["Orders.Create"]}}`,
		"bad section":            `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","contributes":{"config":{"section":"Orders"}}}`,
		"bad stage":              `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","contributes":{"pipeline":[{"middleware":"x","stage":"middle"}]}}`,
		"bad binding":            `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","bindings":[{"name":"Db","type":"connection:relational"}]}`,
	}
	for name, data := range cases {
		if _, err := manifest.Read([]byte(data)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestRequirementsFollowTheVectors(t *testing.T) {
	vectors, err := conformance.CapabilityRequirements()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Valid {
		requirement, ok := manifest.ParseRequirement(c.Input)
		if !ok || requirement.ID != c.ID || int(requirement.Version) != c.Version || requirement.Optional != c.Optional {
			t.Errorf("%q parsed as %+v (ok=%v)", c.Input, requirement, ok)
		}
	}
	for _, text := range vectors.Invalid {
		if _, ok := manifest.ParseRequirement(text); ok {
			t.Errorf("%q must not parse", text)
		}
	}
	declaration, ok := manifest.ParseDeclaration("caching.distributed@2")
	if !ok || declaration.String() != "caching.distributed@2" {
		t.Error("declaration")
	}
	requirement, _ := manifest.ParseRequirement("caching.distributed@^2?")
	if !requirement.IsSatisfiedBy(declaration) || requirement.String() != "caching.distributed@2?" {
		t.Error("satisfaction")
	}
	if other, _ := manifest.ParseDeclaration("caching.distributed@1"); requirement.IsSatisfiedBy(other) {
		t.Error("versions must match")
	}
}

// TestOnlyWasmPluginsMayContributeMiddleware 核对与 .NET 宿主同一规则（RFC-0017「范围」）：只有 Wasm 插件能贡献中间件，
// 进程外与进程内插件的清单写了非空 contributes.pipeline 在读清单时即被拒绝；能力包等非插件照常可以写。
func TestOnlyWasmPluginsMayContributeMiddleware(t *testing.T) {
	const template = `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":%q%s,"contributes":{"pipeline":%s}}`
	const audit = `[{"middleware":"audit","stage":"observe"},{"middleware":"tenant-guard","stage":"tenant"}]`
	for _, isolation := range []manifest.Isolation{manifest.IsolationOutOfProcess, manifest.IsolationInProcess} {
		data := fmt.Sprintf(template, manifest.KindPlugin, fmt.Sprintf(`,"isolation":%q`, isolation), audit)
		_, err := manifest.Read([]byte(data))
		var framework *primitives.FrameworkError
		if !errors.As(err, &framework) || framework.Code != primitives.ErrorCodeInvalidArgument {
			t.Errorf("a %s plugin contributing middleware must be rejected with %s, got %v", isolation, primitives.ErrorCodeInvalidArgument, err)
			continue
		}
		for _, fragment := range []string{"'xihan.a.b'", "'contributes.pipeline'", "audit, tenant-guard", string(isolation)} {
			if !strings.Contains(framework.Message, fragment) {
				t.Errorf("the message for a %s plugin must mention %s: %s", isolation, fragment, framework.Message)
			}
		}
	}

	accepted := map[string]string{
		"wasm plugin":                fmt.Sprintf(template, manifest.KindPlugin, `,"isolation":"wasm"`, audit),
		"capability":                 fmt.Sprintf(template, manifest.KindCapability, "", audit),
		"provider":                   fmt.Sprintf(template, manifest.KindProvider, "", audit),
		"out-of-process, empty list": fmt.Sprintf(template, manifest.KindPlugin, `,"isolation":"out-of-process"`, `[]`),
		"in-process, empty list":     fmt.Sprintf(template, manifest.KindPlugin, `,"isolation":"in-process"`, `[]`),
	}
	for name, data := range accepted {
		if _, err := manifest.Read([]byte(data)); err != nil {
			t.Errorf("%s must be accepted: %v", name, err)
		}
	}

	// 写出同样核对：不生成宿主会拒绝的清单
	written := &manifest.Manifest{
		ID: "xihan.a.b", Version: "1.0.0", Primitives: 1, Stability: primitives.Stable, Kind: manifest.KindPlugin, Isolation: manifest.IsolationOutOfProcess,
		Contributes: &manifest.Contributions{Pipeline: []manifest.PipelineContribution{{Middleware: "audit", Stage: primitives.Observe}}},
	}
	if _, err := manifest.Write(written); err == nil {
		t.Error("writing an out-of-process plugin manifest that contributes middleware must fail")
	}
}

// TestReadFollowsTheSchemaStrictly 核对按 Schema 严格读取：成员名区分大小写，拒绝 null，接口版本只能是数字字符串、
// 只有 requires[].version 可以带 ^，必填成员不能缺，JSON 之后不能有多余内容
func TestReadFollowsTheSchemaStrictly(t *testing.T) {
	const base = `"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability"`
	accepted := map[string]string{
		"requires with ^":       `{` + base + `,"requires":[{"id":"a.b","version":"^2"}]}`,
		"requires without ^":    `{` + base + `,"requires":[{"id":"a.b","version":"2","optional":true}]}`,
		"provides":              `{` + base + `,"provides":[{"id":"a.b","version":"3"}]}`,
		"trailing whitespace":   `{` + base + "}\n\t ",
		"hyphenated operations": `{` + base + `,"contributes":{"operations":["order-lines.list","a1-b2.c3-d4"]}}`,
	}
	for name, data := range accepted {
		if _, err := manifest.Read([]byte(data)); err != nil {
			t.Errorf("%s must be accepted: %v", name, err)
		}
	}
	m, _ := manifest.Read([]byte(accepted["requires with ^"]))
	if m == nil || m.Requires[0].Version != 2 {
		t.Errorf("^2 reads as 2: %+v", m)
	}

	rejected := map[string]string{
		"ID in another case":          `{"ID":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability"}`,
		"Kind in another case":        `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","Kind":"capability"}`,
		"nested member in other case": `{` + base + `,"contributes":{"Operations":["a.b"]}}`,
		"requirement member case":     `{` + base + `,"requires":[{"id":"a.b","Version":"1"}]}`,
		"null package":                `{` + base + `,"package":null}`,
		"null provides":               `{` + base + `,"provides":null}`,
		"null contributes":            `{` + base + `,"contributes":null}`,
		"null array item":             `{` + base + `,"requires":[null]}`,
		"null optional":               `{` + base + `,"requires":[{"id":"a.b","version":"1","optional":null}]}`,
		"null binding required":       `{` + base + `,"bindings":[{"name":"db","type":"queue","required":null}]}`,
		"null stability":              `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":null,"kind":"capability"}`,
		"primitives with ^":           `{"id":"xihan.a.b","version":"1.0.0","primitives":"^1","stability":"stable","kind":"capability"}`,
		"primitives as a number":      `{"id":"xihan.a.b","version":"1.0.0","primitives":1,"stability":"stable","kind":"capability"}`,
		"primitives with a zero":      `{"id":"xihan.a.b","version":"1.0.0","primitives":"01","stability":"stable","kind":"capability"}`,
		"primitives out of range":     `{"id":"xihan.a.b","version":"1.0.0","primitives":"99999999999999999999","stability":"stable","kind":"capability"}`,
		"provides with ^":             `{` + base + `,"provides":[{"id":"a.b","version":"^1"}]}`,
		"requires as a number":        `{` + base + `,"requires":[{"id":"a.b","version":1}]}`,
		"requires with ^^":            `{` + base + `,"requires":[{"id":"a.b","version":"^^1"}]}`,
		"missing stability":           `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","kind":"capability"}`,
		"missing stage":               `{` + base + `,"contributes":{"pipeline":[{"middleware":"audit"}]}}`,
		"missing requirement version": `{` + base + `,"requires":[{"id":"a.b"}]}`,
		"missing config section":      `{` + base + `,"contributes":{"config":{"schema":"x"}}}`,
		"trailing object":             `{` + base + `} {}`,
		"trailing text":               `{` + base + `}x`,
		"operation ends with -":       `{` + base + `,"contributes":{"operations":["orders-.create"]}}`,
		"operation with --":           `{` + base + `,"contributes":{"operations":["order--lines.list"]}}`,
		"operation verb with -":       `{` + base + `,"contributes":{"operations":["orders.create-"]}}`,
		"not an object":               `[]`,
		"null manifest":               `null`,
	}
	for name, data := range rejected {
		_, err := manifest.Read([]byte(data))
		var framework *primitives.FrameworkError
		if !errors.As(err, &framework) || framework.Code != primitives.ErrorCodeInvalidArgument {
			t.Errorf("%s must be rejected with %s, got %v", name, primitives.ErrorCodeInvalidArgument, err)
		}
	}
}

// TestReadRejectsDuplicatesAndEmptyActivation 核对同一层的重复成员名与空的 activation 不合法（RFC-0002 解析规则 2，ADR-0235）
func TestReadRejectsDuplicatesAndEmptyActivation(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate kind":          `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","kind":"provider"}`,
		"duplicate nested member": `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","requires":[{"id":"a.b","id":"c.d","version":"1"}]}`,
		"empty activation":        `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","activation":[]}`,
		"stability by number":     `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"0","kind":"capability"}`,
		"stability capitalised":   `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"Stable","kind":"capability"}`,
		"schema as an object":     `{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability","contributes":{"config":{"section":"XiHan:Orders","schema":{"type":"object"}}}}`,
	} {
		if _, err := manifest.Read([]byte(data)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	m, err := manifest.Read([]byte(`{"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable","kind":"capability"}`))
	if err != nil || !m.ActivatesOnStartup() {
		t.Errorf("a missing activation defaults to onStartup: %v", err)
	}
}
