---
name: xihan-sdk-go-development
description: 设计、实现、重构或对齐 framework-sdk-go 的原语、清单、调用上下文、管道与操作契约时使用，适用于跟随规范修订与 .NET 参考实现的行为；进程外插件运行时、远程 Provider 与门禁发布分别使用 plugin-runtime、remote-provider、testing-release 技能。
---

# framework-sdk-go 开发

包、规范、向量与 .NET 对应类型的对照读 `references/spec-map.md`。

## 先查事实

```bash
git status --short --branch
git log -5 --oneline
git -C spec log -1 --oneline
go list -m github.com/xihanfun/framework-spec/conformance
```

`spec/` 的提交与 `go.mod` 里一致性模块的伪版本应是同一个规范提交；不一致时先按 `AGENTS.md`「升规范指针」对齐，再动代码。

## 先读的规范顺序

1. 目标包对应的 RFC 正文（`spec/rfcs/`），状态行列出所属的一致性档与 ADR。
2. 同一主题的规范性目录：`spec/conventions/`（错误码、Baggage 键、能力 ID、操作 ID、批量优先）、`spec/manifest/manifest.schema.json`。
3. `spec/conformance/vectors/` 下对应的向量：期望值由 .NET 算出，是逐字的验收标准。
4. .NET 参考实现里对应的类型（`../XiHan.Framework/framework/src`），规范写得不够细时看它怎么做。
5. 需要取舍的来龙去脉时读 `../XiHan.Framework/plans/adr/` 里 RFC 引用的 ADR；规范性要求只以正文与向量为准。

## 与 .NET 一致的做法

- 错误码、消息键、参数名与 en-US 模板照 .NET 逐字一致（`kernel-messages.json`、`primitives.DefaultTemplate`）。
- 读 JSON 先过 `internal/jsontext`，值模型与载荷解码用 `internal/jsoncodec`；成员名区分大小写、重复成员拒绝、`null` 落在数值与布尔上不接受，不随 `encoding/json` 的缺省。
- 规范的文法与判定调用一致性模块的 `rules`，不另写一份。
- 可恢复失败返回 `*primitives.Error` / `Result[T]`，编程错误 panic `*primitives.FrameworkError`；登记表与集合有容量上限。

## 规范没写到时

不自行发明。停下来向用户说明差异（规范怎么写、.NET 怎么做、Go 现在怎么做），给出建议的规范文字，在 framework-spec 提交并经 XiHan.Framework 的 ADR 之后再改 SDK。只属于 Go 的选择（规范不规定的 Go 类型）记为 ADR 的补充，写进文档页。

## 完成条件

1. `gofmt -l .` 为空，`go vet ./...` 与 `go test ./...` 通过。
2. 新的或改动的向量都被回放：`vectors_test.go` 的 `replayedVectors` 注明回放它的测试，不能回放的列进 `gaps` 写理由。
3. 新消息键有常量与 `Localization/<区域>.json` 里的模板；文档注释是中文、注明规范章节。
4. 公开 API 或可观察行为变化时，XiHan.Framework 的 `docs/sdks/go.md` 已同步对应章节与「破坏性改动与迁移」（另一个仓库，单独提交）。
5. 一项能力一个提交：`<type>(<scope>): <中文说明>`，说明里写 RFC 与 ADR 编号。
