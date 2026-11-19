# 包、规范、向量与 .NET 的对照

路径相对本仓库根；.NET 路径相对 `../XiHan.Framework/framework/src`。向量由一致性模块内嵌，回放它的测试名见仓库根 `vectors_test.go` 的 `replayedVectors`。

| 包 | 规范 | 向量（`conformance/vectors/`） | .NET 对应 |
| --- | --- | --- | --- |
| `primitives` | RFC-0001（`Result`、`Error`、`Maybe`、线上编码、空白字符、JSON 文本）、RFC-0010「模板」、`conventions/error-codes.md`、`conventions/baggage-keys.md` | `primitives/error-codes.json`、`baggage-keys.json`、`lifecycle-transitions.json`、`message-templates.json`、`wire-encoding.json` | `XiHan.Framework.Primitives`（`Error.cs`、`ResultOfT.cs`、`MessageTemplate.cs`、`BaggageKeys.cs`、`LifecycleState.cs`、`PipelineStage.cs`）；线上编码在 `XiHan.Framework.Kernel/Json/ResultJson.cs` |
| `manifest` | RFC-0002（解析规则 2、「写出」）、`manifest/manifest.schema.json`、`conventions/capability-ids.md` | `manifest/manifests.json`、`primitives/capability-requirements.json` | `XiHan.Framework.Kernel/Capabilities/ManifestReader.cs`、`XiHan.Framework.Primitives/Manifests/` |
| `invocation` | RFC-0003（规则 1 取消、规则 7 追踪标志）、`conventions/baggage-keys.md` | `primitives/trace-context.json` | `XiHan.Framework.Primitives/InvocationContext.cs`、`TraceParent.cs` |
| `pipeline` | RFC-0004（规则 7 顺序约束、规则 8 报错时机） | `pipeline/ordering.json` | `XiHan.Framework.Kernel/Pipeline/PipelineBuilder.cs` |
| `operation` | RFC-0011（规则 2 载荷、规则 5 异常翻译、规则 10 内置步骤与编码）、`conventions/operation-ids.md` | `operations/payload-reasons.json`、`primitives/kernel-messages.json` | `XiHan.Framework.Kernel/Operations/`（`OperationRegistry.cs`、`OperationInvoker.cs`）、`KernelMessages.cs` 与 `Localization/` |
| `internal/jsontext` | RFC-0001「JSON 文本」 | `primitives/json-text.json` | `XiHan.Framework.Kernel/Json/JsonText.cs` |
| `plugin` | RFC-0017、`plugin/handshake.md`、`lifecycle.md`、`invocation.md`、`plugin.proto` | `plugin/*.json`（`wasm-exports.json` 不在本 SDK 认领的档里） | `XiHan.Framework.Plugins.Protocol`（常量、`PluginMessages.cs` 与 `Localization/`）；宿主一侧 `XiHan.Framework.Plugins.OutOfProcess`；手写的 .NET 插件 `framework/samples/EchoPlugin` |
| `caching` | RFC-0019、RFC-0006、`provider/README.md`、`provider/caching-distributed.md`、`conventions/batch-first.md` | `provider/caching-distributed.json` | 宿主一侧适配器 `XiHan.Framework.Caching.Remote`，一致性套件 `XiHan.Framework.Caching.Conformance` |

## 规范规则在一致性模块里的 Go 实现

`github.com/xihanfun/framework-spec/conformance/rules` 是规范文法与判定的 Go 实现，由向量验证。SDK 已经调用的有这些，新代码用到同样的判定时照样调用，与规范的表保持同一份：

- 空白字符集 `IsWhitespace`（RFC-0001「空白字符」，`Error` 的自由文本与握手行共用）。
- 握手行与端点 `ParseHandshakeLine`、`ParseEndpoint`；回调端点列表 `ParseHostAddresses`、`SelectHostEndpoint`。
- 截止时间 `ParseDeadline`、`FormatDeadline`、`EffectiveDeadline`。
- 回调：权限与阶段 `CallbackPermission`、`CallbackPhase`，失败的映射 `MapCallbackStatus`、`StatusReason`；读入对方写来的错误 `ReadProtoError`、`ReadProtoStatus`、`ReadDetails`。
- JSON 文本的共同检查 `CheckJSONText`、`HasNoDuplicateMembers`（`internal/jsontext` 与规则包各自回放同一份 `json-text.json`）；模板占位符 `Placeholders`。

规则包不实现模板的格式化，格式化结果以 `message-templates.json` 为准。
