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
	"caching/cache-keys.json",                 // caching.TestKeysFollowTheVectors
	"manifest/manifests.json",                 // manifest.TestManifestsFollowTheVectors
	"operations/payload-reasons.json",         // operation.TestPayloadReasonsFollowTheVectors
	"pipeline/ordering.json",                  // pipeline.TestPipelineOrderingFollowsTheVectors
	"plugin/binding-properties.json",          // plugin.TestBindingPropertiesFollowTheVectors（reading；其余各节是宿主一侧的）
	"plugin/callback-status.json",             // plugin.TestCallbackStatusFollowsTheVectors
	"plugin/deadlines.json",                   // plugin.TestDeadlinesFollowTheVectors
	"plugin/envelope-times.json",              // plugin.TestEnvelopeTimesFollowTheVectors（valid 与 invalid 是 SDK 的读入，written 是宿主的写法，ADR-0310）
	"plugin/error-reading.json",               // plugin.TestErrorReadingFollowsTheVectors
	"plugin/error-text-writing.json",          // plugin.TestErrorTextWritingFollowsTheVectors（bytes；utf16 与 codePoints 是别的存放方式）
	"plugin/handshake-lines.json",             // plugin.TestHandshakeLinesFollowTheVectors
	"plugin/host-addresses.json",              // plugin.TestHostAddressesFollowTheVectors
	"plugin/plugin-messages.json",             // plugin.TestRuntimeMessagesFollowTheVectors
	"plugin/protocol-versions.json",           // plugin.TestNegotiationFollowsTheVectors
	"plugin/sdk-envelope-times.json",          // plugin.TestSDKEnvelopeTimesFollowTheVectors（纳秒一列，unreadable 另经投递核对，ADR-0310）
	"primitives/baggage-keys.json",            // primitives.TestBaggageKeysFollowTheVectors 等
	"primitives/capability-requirements.json", // manifest.TestRequirementsFollowTheVectors
	"primitives/error-codes.json",             // primitives.TestErrorCodesFollowTheVectors
	"primitives/json-text.json",               // jsontext.TestCheckFollowsTheJSONTextVectors
	"primitives/kernel-messages.json",         // operation.TestKernelMessagesFollowTheVectors
	"primitives/lifecycle-transitions.json",   // primitives.TestLifecycleTransitionsFollowTheVectors
	"primitives/message-templates.json",       // primitives.TestMessageKeysFollowTheTemplateVectors 等
	"primitives/operation-ids.json",           // operation.TestOperationIDsFollowTheVectors
	"primitives/trace-context.json",           // invocation.TestTraceContextFollowsTheVectors
	"primitives/wire-encoding.json",           // primitives.TestResultsFollowTheWireEncodingVectors 等
	"provider/caching-distributed.json",       // caching.TestOperationsReplayTheVectors
}

// claimedProfiles 是本 SDK 认领的一致性档（RFC-0024「一致性档」）：core 所有实现都认领，plugin 是写进程外插件的一侧，
// provider:caching.distributed@1 是 caching 包与 examples/cache 提供的远程 Provider 操作集。
var claimedProfiles = []string{"core", "plugin", "provider:caching.distributed@1"}

// gaps 是认领的档要求回放、本 SDK 却没有回放的向量文件及其理由（RFC-0024「实现钉规范」第 3 条）；目前没有缺口。
var gaps = map[string]string{}

// TestEveryVectorOfTheClaimedProfilesIsReplayed 按 go.mod 钉住的一致性模块内嵌的档表核对认领的档（RFC-0024「一致性档」，ADR-0242）：
// 认领的档都有定义，它们要求的向量要么被回放、要么列为缺口，缺口只能是认领的档要求而没有回放的向量，回放的文件都存在；
// 规范在认领的档里新增向量时这里失败，而不是悄悄不回放。回放认领的档以外的向量是允许的。
func TestEveryVectorOfTheClaimedProfilesIsReplayed(t *testing.T) {
	table, err := conformance.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	names, err := conformance.Names()
	if err != nil {
		t.Fatal(err)
	}

	// 未知的档单独报出，其余的档照常核对，免得一个错的档名盖住别的问题
	var defined, undefined []string
	for _, id := range claimedProfiles {
		if _, ok := table.Profile(id); ok {
			defined = append(defined, id)
		} else {
			undefined = append(undefined, id)
		}
	}
	if len(undefined) > 0 {
		t.Errorf("claimed profiles that the spec does not define: %s", sortedList(undefined))
	}
	required, err := table.RequiredVectors(defined...)
	if err != nil {
		t.Fatal(err)
	}

	isRequired := map[string]bool{}
	for _, name := range required {
		isRequired[name] = true
	}
	isReplayed := map[string]bool{}
	for _, name := range replayedVectors {
		isReplayed[name] = true
	}
	exists := map[string]bool{}
	for _, name := range names {
		exists[name] = true
	}

	var uncovered []string
	for _, name := range required {
		if _, isGap := gaps[name]; !isReplayed[name] && !isGap {
			uncovered = append(uncovered, name)
		}
	}
	if len(uncovered) > 0 {
		t.Errorf("vector files of the claimed profiles that are neither replayed nor listed as gaps: %s", sortedList(uncovered))
	}

	var outside, replayedGaps []string
	for name := range gaps {
		if !isRequired[name] {
			outside = append(outside, name)
		}
		if isReplayed[name] {
			replayedGaps = append(replayedGaps, name)
		}
	}
	if len(outside) > 0 {
		t.Errorf("gaps outside the claimed profiles: %s", sortedList(outside))
	}
	if len(replayedGaps) > 0 {
		t.Errorf("gaps that are replayed; remove them from gaps: %s", sortedList(replayedGaps))
	}

	var missing []string
	for _, name := range replayedVectors {
		if !exists[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("replayed vector files that the spec does not have: %s", sortedList(missing))
	}
}

// sortedList 把文件名或档名排序后以逗号连接，失败信息与顺序无关。
func sortedList(names []string) string {
	slices.Sort(names)
	return strings.Join(names, ", ")
}
