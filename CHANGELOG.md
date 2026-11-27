# 更新日志 · XiHan Framework SDK for Go

本页记录 `github.com/xihanfun/framework-sdk-go` 各版本中使用者可感知的变更，每条标注 **新增 / 修复 / 优化 / 调整 / 移除** 类别；仓库自身的 CI、测试与构建脚本不列入。版本就是仓库的 git 标签，详细说明见文档站 [Go SDK](https://framework.docs.xihanfun.com/sdks/go) 的「破坏性改动与迁移」。

## 未发布

### 升级须知

- `plugin.Envelope.Time` 由 `string` 改为 `*time.Time`（nil 是没有），`plugin.FromProtoEnvelope` 改为返回 `(Envelope, bool)`：发布处把 `Time: t.UTC().Format(time.RFC3339Nano)` 改成 `Time: &t`，读 `envelope.Time` 的先判 nil，调用 `FromProtoEnvelope` 的接住第二个返回值（ADR-0306）
- `plugin.Binding` 新增 `Properties` 后不再可比较，经 `fmt`、`slog` 与 `json.Marshal` 写出时只有绑定名、类型与属性名：`==` 与作 map 键的地方改为逐字段比较，不带字段名的复合字面量补上字段名，要主值的直接读 `binding.Value`（ADR-0285）
- `plugin.Envelope` 新增 `DataSchema`：不带字段名的 `Envelope` 复合字面量补上字段名（ADR-0305）
- 超过 128 个字符的操作 ID 不再合法，`operation.Add` 返回 `PRIMITIVES_VALIDATION_001`，读清单为 `kernel.manifest.invalid`：把 ID 缩到 128 个字符以内（ADR-0276）
- 操作的响应写不成合法的 JSON 文本（字符串不是合法的 UTF-8，`json.Marshaler` 原文有非法字节、孤立代理项的转义或重复成员，超过 64 层）时以 `KERNEL_UNEXPECTED_001` + `kernel.invocation.unhandled_exception` 失败、没有载荷：外部字节放进响应之前先用 `utf8.ValidString` 核对或 `strings.ToValidUTF8` 换掉（ADR-0318、ADR-0319）
- `Result[T].MarshalJSON` 与 `(*Error).MarshalJSON` 遇到不是合法 UTF-8 的字符串（含编成 UTF-8 的孤立代理项）、从结果的根算起超过 64 层或原样嵌进去的文本有重复成员时返回 `PRIMITIVES_VALIDATION_001` 的 `FrameworkError`、不交出文本：放进结果与错误之前保证字符串是合法的 UTF-8（ADR-0320）
- 已登记 Baggage 键的值不是合法的 UTF-8（含孤立代理项）时，外部输入丢弃这个成员、`Context.WithBaggage` panic：从外部输入拼值之前先用 `utf8.ValidString` 核对（ADR-0321）
- `caching.IsKey` 与 `ValidateKey` 拒收不是合法 UTF-8 的键（`CACHING_VALIDATION_001`）：自己拼键的地方保证键是合法的 UTF-8（ADR-0316）
- 线上编码遇到不是合法 JSON 数字的 `json.Number`（含带空白的 `" 1"`）报错，不再原样写出或写成 `0`：放进 `Result` 的值与错误细节之前保证它是数字文本
- `errors.Is` / `errors.As` 沿 `Error.Inner` 往里匹配：只想比较最外层错误码的地方改为直接比较 `Code`
- `Maybe` 作模板参数或进日志时的文本由 `{值 true}` 变为 `Some(值)` / `None`：匹配旧文本的地方改掉
- 插件进程里操作 panic 与响应写不出的日志改写进运行时的日志器：要收这些日志的给 `Options.Logger`，不再靠替换 `slog.Default()`
- 生命周期回调与 `caching.Store` 返回的、用 `%w` 包着的 `*primitives.Error` 原样交给宿主：不想把内部错误带给宿主的不要用 `%w` 包它
- `caching` 的九个操作都先在适配层校验键，`try-remove` 在键与 `expected` 都不合法时改报键（`CACHING_VALIDATION_001`）：自己实现的 `Store` 不必再校验键，按错误内容断言的测试更新期望
- 照抄 `examples/cache` 消息目录的插件，把 `caching.remote.key_invalid` 换成新模板（按 UTF-8 字节计、补上孤立的代理项），否则与宿主 `Caching.Remote` 的目录冲突（`LOCALIZATION_CONFLICT_001`）（ADR-0276、ADR-0321）

### 变更

- **新增** `plugin.Binding.Properties`：`Configure` 下发的绑定属性交给配置回调（ADR-0285）
- **新增** `plugin.Envelope.DataSchema`：发布与投递原样带上 CloudEvents `dataschema`（ADR-0305）
- **新增** `plugin.EventDelivery` 与 `EventDeliveryOf(ctx)`：事件处理器拿得到这次投递的方式与尝试次数
- **新增** `plugin.Options.Logger` 与 `operation.WithLogger`：运行时、调用器与插件自己的日志进同一个日志器
- **新增** `invocation.NewWithCancel` 与 `Context.WithContext`：插件运行时每次调用与投递结束时归还截止时间的定时器，长寿命的 parent 上不再越积越多
- **新增** `operation.Registry.Len`、`operation.MaxIDLength`、`primitives.NewFrameworkError`、`primitives.BindMaybe`、`Maybe.String` 与 `(*Error).Unwrap`
- **修复** 宿主写来的信封 `time` 读不出时投递以 `InvalidArgument` 拒绝（ADR-0306）
- **修复** 插件写给宿主的错误细节写不出（非法 UTF-8、超过 64 层、重复成员）时这个错误不带细节、其余字段照写，不再写成 U+FFFD 或 `\ud800`（ADR-0320）
- **修复** 操作的响应里嵌着写不出的 `Result` 或 `Error` 时以 `kernel.invocation.unhandled_exception` 结束、不带 `inner`（ADR-0320）
- **修复** 插件写给宿主的错误文案（含内部错误的）不是合法的 UTF-8 时按最大子部分换成 U+FFFD 写出，整个回调、发布与应答不再因它编不出（ADR-0339）
- **修复** 回调元数据里的 Baggage 值（幂等键）不是合法的 UTF-8 时在本地以 `plugins.process.failed` 结束、不发请求，`reason` 点名那个键，不再是 grpc-go 的编码错误（ADR-0347）
- **修复** 有一处报 `kernel.operation.unknown` 漏了参数 `id`，文案填不出操作 ID
- **修复** 依赖 `golang.org/x/net` 升到 `v0.60.0`，修掉 4 个经 gRPC 服务端可达的 HTTP/2 漏洞；`go.mod` 的 `go` 指令随之写成 `1.26.0`
- **调整** 清单 `activation` 里 `onEvent:` 的模式可以带下划线（ADR-0276）
- **调整** `examples/echo` 的 `events.list` 回显处理器拿到的信封（`envelopes`），黑盒运行器的用例 `event-envelope` 通过（ADR-0327）
- **调整** `Host` 关闭之后（`Serve` 返回后）与被关闭切断的回调不再经 grpc-go，`reason` 由 `Cancelled: grpc: the client connection is closing` 改为与其他 SDK 相同的 `Unavailable: the host client is closed`；`Host.Close` 可以重复调用（ADR-0349）
- **调整** 规范指针与一致性模块升到规范 `2bf33b1`

## v0.2.0-rc.1（2026-11-21）

- **新增** 首个对外预发布版，经 Go 模块代理发布：原语、清单、调用上下文、管道、操作契约、进程外插件运行时与 `caching.distributed@1` 远程 Provider 的插件侧，钉规范 `v1.0.0-rc.1`，认领一致性档 `core`、`plugin` 与 `provider:caching.distributed@1`
