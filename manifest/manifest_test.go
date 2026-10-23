package manifest_test

import (
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
