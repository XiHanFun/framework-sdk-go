package sdk_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/xihanfun/framework-spec/conformance"
)

// replayedVectors 是本 SDK 的测试回放的向量文件（相对 vectors/，正斜杠分隔），注明回放它的测试；
// 来源是 go.mod 钉住的一致性模块内嵌的那一份，测试经它的类型化入口读取。
var replayedVectors = []string{
	"manifest/manifests.json",                 // manifest.TestManifestsFollowTheVectors
	"operations/payload-reasons.json",         // operation.TestPayloadReasonsFollowTheVectors
	"pipeline/ordering.json",                  // pipeline.TestPipelineOrderingFollowsTheVectors
	"plugin/callback-status.json",             // plugin.TestCallbackStatusFollowsTheVectors
	"plugin/deadlines.json",                   // plugin.TestDeadlinesFollowTheVectors
	"plugin/error-reading.json",               // plugin.TestErrorReadingFollowsTheVectors
	"plugin/handshake-lines.json",             // plugin.TestHandshakeLinesFollowTheVectors
	"plugin/host-addresses.json",              // plugin.TestHostAddressesFollowTheVectors
	"plugin/plugin-messages.json",             // plugin.TestRuntimeMessagesFollowTheVectors
	"plugin/protocol-versions.json",           // plugin.TestNegotiationFollowsTheVectors
	"primitives/baggage-keys.json",            // primitives.TestBaggageKeysFollowTheVectors 等
	"primitives/capability-requirements.json", // manifest.TestRequirementsFollowTheVectors
	"primitives/error-codes.json",             // primitives.TestErrorCodesFollowTheVectors
	"primitives/json-text.json",               // jsontext.TestCheckFollowsTheJSONTextVectors
	"primitives/kernel-messages.json",         // operation.TestKernelMessagesFollowTheVectors
	"primitives/lifecycle-transitions.json",   // primitives.TestLifecycleTransitionsFollowTheVectors
	"primitives/message-templates.json",       // primitives.TestMessageKeysFollowTheTemplateVectors 等
	"primitives/trace-context.json",           // invocation.TestTraceContextFollowsTheVectors
	"primitives/wire-encoding.json",           // primitives.TestResultsFollowTheWireEncodingVectors 等
	"provider/caching-distributed.json",       // caching.TestOperationsReplayTheVectors
}

// exemptedVectors 是本 SDK 不回放的向量文件及其理由。
var exemptedVectors = map[string]string{
	"plugin/wasm-exports.json": "Wasm ABI exports are checked by hosts; this SDK has no Wasm plugin runtime.",
	"security/webauthn.json":   "WebAuthn verification is host-side (RFC-0008); this SDK does not verify credentials.",
}

// TestEveryVectorIsReplayedOrExempted 核对一致性模块内嵌的全部向量文件恰好是回放集合与豁免集合之并（ADR-0242）：
// 两个集合不相交，列出的文件都存在，存在的文件都在其一；规范新增或删除向量文件时这里失败，而不是悄悄不回放。
func TestEveryVectorIsReplayedOrExempted(t *testing.T) {
	names, err := conformance.Names()
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]bool{}
	for _, name := range names {
		existing[name] = true
	}

	listed := map[string]bool{}
	for _, name := range replayedVectors {
		listed[name] = true
	}
	var both []string
	for name := range exemptedVectors {
		if listed[name] {
			both = append(both, name)
		}
		listed[name] = true
	}
	slices.Sort(both)
	if len(both) > 0 {
		t.Errorf("vector files both replayed and exempted: %s", strings.Join(both, ", "))
	}

	var neither []string
	for _, name := range names {
		if !listed[name] {
			neither = append(neither, name)
		}
	}
	if len(neither) > 0 {
		t.Errorf("spec vector files neither replayed nor exempted: %s", strings.Join(neither, ", "))
	}

	var missing []string
	for name := range listed {
		if !existing[name] {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("listed vector files that the spec does not have: %s", strings.Join(missing, ", "))
	}
}
