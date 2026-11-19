---
name: xihan-sdk-go-create-pr
description: 为 framework-sdk-go 准备、审查或创建 Pull Request 时使用，适用于生成 PR 标题与正文、比较分支或执行 gh pr create；普通代码审查不使用，只有用户明确要求时才推送与创建。
---

# framework-sdk-go 创建 PR

## 前置检查

- 检查分支、上游、工作区、merge/rebase 和冲突；非正常分支状态不创建 PR。
- base 默认 `dev`，用户另行指定时以其为准；不默认改成 `main`。
- 用 `git log <base>..HEAD`、`git diff --stat <base>...HEAD` 和完整 diff 审查整条分支。
- 核对 `gofmt -l .`、`go vet ./...`、`go test ./...` 与生成物检查的结果；`spec/` 与 `go.mod` 的一致性模块是否同一提交、规范提交是否已推送（否则 CI 解析不到伪版本）。

## PR 内容

仓库没有 PR 模板，正文按下面几段写：

1. **关联 Issue**：`Closes #<编号>`，没有就写「无」。
2. **变更说明**：使用者可感知的结果，引用对应的 RFC 与 ADR。
3. **影响范围**：涉及的包（`primitives`、`plugin` 等）与一致性档，不堆文件名。
4. **自测**：只列实际执行过的命令与结果（门禁、运行器测试、Linux 或 Windows）。
5. **破坏性变更**：公开 API、错误码与消息键、线上行为的变化与迁移方式；文档页「破坏性改动与迁移」是否已同步；没有就写「无」。

标题使用 Conventional Commit 形态：`<type>(<scope>): <中文说明>`。

只要草稿时不写远程。明确要求创建 PR 时才推送当前分支并执行 `gh pr create --base dev`；禁止 force push、合并或改写其他远端分支。创建后返回 PR 链接。
