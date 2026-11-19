---
name: xihan-sdk-go-commit-msg
description: 根据 framework-sdk-go 当前暂存区生成一行提交信息时使用，适用于「提交信息」「commit msg」「msg」或提交前概括 staged changes；只生成消息，不负责暂存、解决冲突或执行提交。
---

# framework-sdk-go 提交信息

1. 检查 `git status --short --branch`；存在冲突、merge 或 rebase 时先报告，不生成完成态消息。
2. 只读取 `git diff --cached --stat`、完整 staged diff 和最近 20 条提交。暂存区为空时不从未暂存内容猜测。
3. 将全部暂存变化归纳成一个意图，输出一行 Conventional Commit。

格式：`<type>(<scope>): <中文说明>`。type 使用 `feat`、`fix`、`refactor`、`perf`、`docs`、`style`、`test`、`build`、`ci`、`chore`、`revert`。

scope 用本仓库的包名：`primitives`、`manifest`、`invocation`、`pipeline`、`operation`、`plugin`、`caching`、`examples`；向量覆盖与运行器测试用 `conformance`；规范指针与一致性模块的升级用 `spec`；另有 `docs`、`build`、`ci`。跨多个包的同一项改动取主要的包，或省略 scope。

说明写使用者可感知的结果，引用对应的 RFC 与 ADR 编号（如「（ADR-0245）」）；随改动升了规范指针时写明「规范指针与 go.mod 的一致性模块升到 <短哈希>」；有破坏性改动时在说明里点明，并确认文档页的「破坏性改动与迁移」已写。只要求消息时最终只输出一行标题，不附解释或候选列表。
