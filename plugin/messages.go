package plugin

import (
	"embed"
	"fmt"
	"maps"

	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// runtimeLocalization 内嵌 SDK 运行时的消息目录：invocation.md §8 的 7 个键，10 种区域，译文与 .NET 参考实现
// XiHan.Framework.Plugins.Protocol 的语言文件相同。
//
//go:embed Localization
var runtimeLocalization embed.FS

// RuntimeCatalogName 是 SDK 运行时消息目录的目录名。
const RuntimeCatalogName = "plugins"

// runtimeCatalog 是读出的运行时目录；内嵌的文件读不出是构建错误，初始化时即 panic。
var runtimeCatalog = mustLoadRuntimeCatalog()

// 这些键的 en-US 模板登记为默认模板：进程默认的解析器解析不出时 Error.Message 用它们（RFC-0010）
func init() {
	primitives.RegisterDefaultTemplates(runtimeCatalog.Locales[primitives.DefaultCatalogLocale])
}

// mustLoadRuntimeCatalog 读内嵌的运行时目录。
func mustLoadRuntimeCatalog() Catalog {
	catalog, err := LoadCatalog(RuntimeCatalogName, runtimeLocalization, "Localization")
	if err != nil {
		panic(&primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: "The embedded runtime catalog cannot be read.", Cause: err})
	}
	return catalog
}

// RuntimeCatalog 返回 SDK 运行时消息目录的副本（invocation.md §8）：宿主有同样的目录，运行时不随握手发送它，
// 只供本地渲染（Catalog.Resolver 已经合并了它）。
func RuntimeCatalog() Catalog {
	locales := make(map[string]map[string]string, len(runtimeCatalog.Locales))
	for locale, templates := range runtimeCatalog.Locales {
		locales[locale] = maps.Clone(templates)
	}
	return Catalog{Name: RuntimeCatalogName, Locales: locales}
}

// 生命周期与回调的阶段名，写进 plugins.process.failed 的 phase（lifecycle.md §7，一律小写）。
const (
	// phaseConfigure 是 Configure。
	phaseConfigure = "configure"
	// phaseStart 是 Start。
	phaseStart = "start"
	// phaseStop 是 Stop 与收尾时的停止回调。
	phaseStop = "stop"
	// phaseHealth 是健康回调。
	phaseHealth = "health"
	// phaseInvoke 是读入的错误码不合文法时的阶段（invocation.md §3）。
	phaseInvoke = "invoke"
	// phasePublish 是 Host.Publish。
	phasePublish = "publish"
)

// 写进 reason 的固定英文文本（plugin-messages.json 的 reasons，不本地化）。
const (
	// reasonConfigurationNotObject 是配置节不是 JSON 对象（lifecycle.md §7.2）。
	reasonConfigurationNotObject = "the configuration is not a JSON object"
	// reasonNoCallbackEndpoint 是 XIHAN_PLUGIN_HOST_ADDRESS 缺失、不合法或没有能连的项（handshake.md §4）。
	reasonNoCallbackEndpoint = "no supported callback endpoint"
	// reasonHostFailureWithoutError 是宿主写来的 Status.ok 为假却没有 error（invocation.md §3）。
	reasonHostFailureWithoutError = "the host reported a failure without an error"
	// reasonPanic 是回调以 panic 结束（lifecycle.md §7.6）。
	reasonPanic = "panic"
)

// reasonInvalidErrorCode 是对方写来的错误码不合文法时的 reason（invocation.md §3）。
func reasonInvalidErrorCode(code string) string { return "invalid error code '" + code + "'" }

// reasonResultCount 是 Host.Publish 的结果数与信封数不同时的 reason（invocation.md §5）。
func reasonResultCount(results, envelopes int) string {
	return fmt.Sprintf("the host returned %d results for %d envelopes", results, envelopes)
}

// processFailed 创建 PLUGINS_UNEXPECTED_001 + plugins.process.failed，参数 {id, phase, reason}。
func processFailed(id, phase, reason string) *primitives.Error {
	return primitives.Unexpected(ErrorCodeUnexpected, MessageUnexpected, map[string]any{"id": id, "phase": phase, "reason": reason}, nil)
}

// processUnavailable 创建 PLUGINS_UNAVAILABLE_001 + plugins.process.unavailable，参数 {id, state}，state 是状态名（PascalCase）。
func processUnavailable(id string, state primitives.LifecycleState) *primitives.Error {
	return primitives.Unavailable(ErrorCodeUnavailable, MessageUnavailable, map[string]any{"id": id, "state": state.String()})
}

// eventUnsubscribed 创建 KERNEL_NOTFOUND_001 + plugins.event.unsubscribed，参数 {id, topic, type, mode}，空方式写 competing。
func eventUnsubscribed(id string, envelope Envelope, mode SubscriptionMode) *primitives.Error {
	return primitives.NotFound(operation.ErrorCodeNotFound, MessageUnsubscribed, map[string]any{"id": id, "topic": envelope.Topic, "type": envelope.Type, "mode": string(mode.orCompeting())})
}

// hostUnavailable 创建 PLUGINS_UNAVAILABLE_001 + plugins.host.unavailable，参数 {id, reason}。
func hostUnavailable(id, reason string) *primitives.Error {
	return primitives.Unavailable(ErrorCodeUnavailable, MessageHostUnavailable, map[string]any{"id": id, "reason": reason})
}

// callbackDenied 创建 PLUGINS_DENIED_002 + plugins.callback.denied，参数 {id, permission}。
func callbackDenied(id, permission string) *primitives.Error {
	return primitives.Denied(ErrorCodeCallbackDenied, MessageCallbackDenied, map[string]any{"id": id, "permission": permission})
}

// tenantDenied 创建 PLUGINS_DENIED_002 + plugins.callback.tenant_denied，参数 {id, tenant}。
func tenantDenied(id, tenant string) *primitives.Error {
	return primitives.Denied(ErrorCodeCallbackDenied, MessageTenantDenied, map[string]any{"id": id, "tenant": tenant})
}
