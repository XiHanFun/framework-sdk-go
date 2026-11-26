# 更新日志 · XiHan Framework SDK for Go

本文件记录 framework-sdk-go 各版本中使用者可感知的变更，每条标注 **新增 / 修复 / 优化 / 调整 / 移除** 类别；仓库自身的 CI、测试与构建脚本不列入。迁移的细节见文档站 [Go SDK](https://framework.docs.xihanfun.com/sdks/go) 的「破坏性改动与迁移」。

## 未发布

### 升级须知

- `Result[T].MarshalJSON` 与 `(*Error).MarshalJSON` 遇到不是合法 UTF-8 的字符串（含编成 UTF-8 的孤立代理项）、从结果的根算起超过 64 层或原样嵌进去的文本有重复成员时返回 `PRIMITIVES_VALIDATION_001` 的 `FrameworkError`、不交出文本：放进结果与错误之前保证字符串是合法的 UTF-8（ADR-0320）
- 已登记 Baggage 键的值不是合法的 UTF-8（含孤立代理项）时，外部输入丢弃这个成员、`Context.WithBaggage` panic：从外部输入拼值之前先用 `utf8.ValidString` 核对（ADR-0321）
- 照抄 `examples/cache` 消息目录的插件，把 `caching.remote.key_invalid` 改成补上孤立代理项的新模板，否则与宿主 `Caching.Remote` 的目录冲突（`LOCALIZATION_CONFLICT_001`）（ADR-0321）

### 变更

- **修复** 插件写给宿主的错误细节写不出（非法 UTF-8、超过 64 层、重复成员）时这个错误不带细节、其余字段照写，不再写成 U+FFFD 或 `\ud800`（ADR-0320）
- **修复** 操作的响应里嵌着写不出的 `Result` 或 `Error` 时以 `kernel.invocation.unhandled_exception` 结束、不带 `inner`（ADR-0320）
- **调整** `examples/echo` 的 `events.list` 回显处理器拿到的信封（`envelopes`），黑盒运行器的用例 `event-envelope` 通过（ADR-0327）
- **调整** 规范指针与一致性模块升到 `6bd152b`（ADR-0320、ADR-0321、ADR-0327、ADR-0328）

## v0.2.0-rc.1（2026-11-21）

### 升级须知

- 首个对外版本，是预发布版：`go get github.com/xihanfun/framework-sdk-go@v0.2.0-rc.1`
- 钉在规范 `v1.0.0-rc.1`，一致性模块 `conformance/v1.0.0-rc.1`

### 变更

- **新增** 原语层 `primitives`：`Error` 与错误码、`Result[T]`、`Maybe[T]`、`ID[Tag, V]`、生命周期、管道阶段、Baggage 键、消息键与模板
- **新增** 清单 `manifest`、调用上下文 `invocation`、十一阶段的执行管道 `pipeline` 与操作契约 `operation`
- **新增** 进程外插件运行时 `plugin`（`Serve` 与宿主回调客户端 `Host`），协议代码 `plugin/v1`
- **新增** `caching.distributed@1` 远程 Provider 的插件侧 `caching`，示例插件 `examples/echo` 与 `examples/cache`
