---
name: xihan-sdk-go-changelog-collect
description: 收集 framework-sdk-go 两个标签或提交之间的用户可感知变化、写进 XiHan.Framework 文档站 Go SDK 页「破坏性改动与迁移」时使用；不改版本、不打标签、不推送。
---

# framework-sdk-go 变更整理

## 事实源

- 用户指定的起止标签或提交；没有指定时先列出最近的标签（`git tag --sort=-creatordate`）与当前 `HEAD`，仓库还没有标签时从用户给的提交开始。
- 范围内的提交（`git log <起>..<止> --oneline`）、实际 diff、导出标识符的变化（`git diff <起>..<止> -- '*.go' ':!*_test.go' ':!plugin/v1'`）。
- 提交说明里引用的 RFC 与 ADR（`spec/rfcs/`、`../XiHan.Framework/plans/adr/`）。
- 去处：`../XiHan.Framework/docs/sdks/go.md` 的「破坏性改动与迁移」一节（另一个仓库，单独提交）。

## 收录规则

- 只收录 SDK 使用者可感知的变化：公开 API、错误码与消息键、线上编码、载荷与清单的读写、插件运行时对宿主的行为、默认值与时限。
- 纯测试、CI、内部重构、规范指针升级不单独收录，除非它改变了可观察行为。
- 沿用该节现有条目的写法：一条一个主题，先写现在的行为与依据（RFC、ADR），再写旧版怎样，最后写「迁移：」给出调用方要改的地方；不需要改代码的行为变化合在「插件运行时的行为变化」那样的条目里。
- 同一底层修复影响多个包时合并成一条。
- 新条目追加在该节末尾；同时检查「与宿主共用的行为」等章节是否需要同步改写。
- 文档正文不写版本号与提交哈希。

本技能只改文档页，不改代码、不打标签、不推送；文档站的构建在 XiHan.Framework 根执行 `pnpm --dir docs build`。
