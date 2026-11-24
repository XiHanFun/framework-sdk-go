# 包、规范、向量与 .NET 的对照

路径相对本仓库根；.NET 路径相对 `../XiHan.Framework/framework/src`。向量由一致性模块内嵌，回放它的测试名见仓库根 `vectors_test.go` 的 `replayedVectors`。

| 包 | 规范 | 向量（`conformance/vectors/`） | .NET 对应 |
| --- | --- | --- | --- |
| `primitives` | RFC-0001（`Result`、`Error`、`Maybe`、线上编码、空白字符、JSON 文本）、RFC-0010「模板」、`conventions/error-codes.md`、`conventions/baggage-keys.md` | `primitives/error-codes.json`、`baggage-keys.json`、`lifecycle-transitions.json`、`message-templates.json`、`wire-encoding.json` | `XiHan.Framework.Primitives`（`Error.cs`、`ResultOfT.cs`、`MessageTemplate.cs`、`BaggageKeys.cs`、`LifecycleState.cs`、`PipelineStage.cs`）；线上编码在 `XiHan.Framework.Kernel/Json/ResultJson.cs` |
| `manifest` | RFC-0002（解析规则 2、「写出」）、`manifest/manifest.schema.json`、`conventions/capability-ids.md` | `manifest/manifests.json`、`primitives/capability-requirements.json` | `XiHan.Framework.Kernel/Capabilities/ManifestReader.cs`、`XiHan.Framework.Primitives/Manifests/` |
| `invocation` | RFC-0003（规则 1 取消、规则 7 追踪标志）、`conventions/baggage-keys.md` | `primitives/trace-context.json` | `XiHan.Framework.Primitives/InvocationContext.cs`、`TraceParent.cs` |
| `pipeline` | RFC-0004（规则 7 顺序约束、规则 8 报错时机） | `pipeline/ordering.json` | `XiHan.Framework.Kernel/Pipeline/PipelineBuilder.cs` |
| `operation` | RFC-0011（「操作 ID」、规则 2 载荷、规则 5 异常翻译、规则 10 内置步骤与编码）、`conventions/operation-ids.md` | `operations/payload-reasons.json`、`primitives/kernel-messages.json`、`primitives/operation-ids.json` | `XiHan.Framework.Kernel/Operations/`（`OperationRegistry.cs`、`OperationInvoker.cs`）、`KernelMessages.cs` 与 `Localization/` |
| `internal/jsontext` | RFC-0001「JSON 文本」 | `primitives/json-text.json` | `XiHan.Framework.Kernel/Json/JsonText.cs` |
| `plugin` | RFC-0017、`plugin/handshake.md`、`lifecycle.md`、`invocation.md`、`plugin.proto` | `plugin/*.json`（`wasm-exports.json` 不在本 SDK 认领的档里） | `XiHan.Framework.Plugins.Protocol`（常量、`PluginMessages.cs` 与 `Localization/`）；宿主一侧 `XiHan.Framework.Plugins.OutOfProcess`；手写的 .NET 插件 `framework/samples/EchoPlugin` |
| `caching` | RFC-0019、RFC-0006、`provider/README.md`、`provider/caching-distributed.md`、`conventions/batch-first.md` | `provider/caching-distributed.json`、`caching/cache-keys.json`（只有键，前缀属于远程插件不实现的键管理） | 宿主一侧适配器 `XiHan.Framework.Caching.Remote`，一致性套件 `XiHan.Framework.Caching.Conformance` |

## 规范规则在一致性模块里的 Go 实现

`github.com/xihanfun/framework-spec/conformance/rules` 是规范文法与判定的 Go 实现，由向量验证。SDK 对它有两种用法，以当前代码为准（`grep -rn "rules\." --include=*.go . | grep -v "^./spec"`）；新代码用到某个判定时先看 `rules` 里有没有，有就调用，与规范的表保持同一份。

### SDK 直接调用 rules 的

- 空白字符集 `IsWhitespace`（RFC-0001「空白字符」）：`primitives` 里 `Error` 的自由文本、`plugin` 的端点地址。
- 操作 ID `IsOperationID`、上限 `MaxOperationIDLength`（`conventions/operation-ids.md`，不超过 128 个字符）：`operation.IsID`、`operation.MaxIDLength` 与 `manifest` 的 `contributes.operations` 都是它，SDK 里没有另一份正则。
- 清单的激活事件 `IsActivationEvent`（RFC-0002 解析规则 2 第 3 步，`plugin/lifecycle.md` §3）：`manifest` 读取 `activation`；`onEvent:` 的模式可以带下划线，`onOperation:` 的不行。
- 缓存键 `IsCacheKey`、上限 `MaxCacheKeyBytes`（RFC-0006「存储契约」，按 UTF-8 字节计）：`caching.IsKey` 与 `caching.MaxKeyLength`。键前缀的 `IsCacheKeyPrefix` 没有用到，远程插件不实现键管理。
- 主题 `IsTopic`（RFC-0009）：`plugin` 核对订阅声明。
- 协议版本协商 `NegotiateVersion`：`plugin` 的 Handshake。
- 握手行与端点 `ParseHandshakeLine`、`ParseEndpoint`；选回调端点 `SelectHostEndpoint`。
- 截止时间 `ParseDeadline`、`FormatDeadline`、`EffectiveDeadline`。
- 回调：权限与阶段 `CallbackPermission`、`CallbackPhase`，失败的映射 `MapCallbackStatus`；读入对方写来的错误 `ReadProtoError`、`ReadProtoStatus`。

`ParseHostAddresses`、`ReadDetails`、`StatusReason` 没有被 SDK 直接调用：它们分别在 `SelectHostEndpoint`、`ReadProtoError`、`MapCallbackStatus` 里面起作用。

### SDK 自己实现、靠回放向量验证的

这些判定 `rules` 里也有对应的函数，SDK 没有调用它，而是自己实现并回放同一份向量（测试名见 `vectors_test.go` 的 `replayedVectors`）；改它们时以向量为准，不拿 `rules` 的实现当事实源。

| 判定 | SDK 的实现 | 向量 |
| --- | --- | --- |
| 错误码文法与 KIND | `primitives.IsErrorCode`、`ErrorCodeKind` | `primitives/error-codes.json` |
| Baggage 键的文法、登记表与值 | `primitives.IsBaggageKeyWellFormed`、`IsBaggageKeyRegistered`、`IsBaggageValueAcceptable` | `primitives/baggage-keys.json` |
| 消息键、模板占位符与格式化 | `primitives.IsMessageKey`、`Placeholders`、`FormatMessage` 一族 | `primitives/message-templates.json`、`primitives/kernel-messages.json` |
| 生命周期迁移 | `primitives.LifecycleState.CanTransitionTo` | `primitives/lifecycle-transitions.json` |
| `Result` 与 `Error` 的线上编码 | `primitives` 的 `MarshalJSON` / `UnmarshalJSON` | `primitives/wire-encoding.json` |
| traceparent | `invocation.ParseTraceParent`、`Context.TraceParent` | `primitives/trace-context.json` |
| 清单 ID、能力 ID 与能力依赖，清单其余成员的文法（操作 ID 与激活事件除外，见上） | `manifest.IsManifestID`、`IsCapabilityID` 与读取 | `manifest/manifests.json`、`primitives/capability-requirements.json` |
| 管道阶段与排序 | `primitives.PipelineStage`、`pipeline.Builder` | `pipeline/ordering.json` |
| JSON 文本的共同检查 | `internal/jsontext.Parse` / `Check` | `primitives/json-text.json` |

测试里拿 `rules` 当独立实现对照的有：`rules.CheckJSONText`（`internal/jsontext` 的向量回放逐例比较两边的结果，`primitives` 回放线上编码的重复成员时也用它；`rules.HasNoDuplicateMembers` 已标弃用，staticcheck 会报，不再用）与 `rules.Placeholders`（`primitives`、`operation` 的向量回放）。

规则包不实现模板的格式化，格式化结果以 `message-templates.json` 为准。
