---
name: xihan-sdk-go-version-release
description: 为 framework-sdk-go 准备版本、核对规范钉在标签上、打或推送版本标签时使用；适用于选定版本号之后的核对清单与 RFC-0024 的钉规范规则。只有用户明确要求时才打标签、推送或快进 main。
---

# framework-sdk-go 版本发布

## 版本真源

- 版本就是仓库的 git 标签：普通的 `vX.Y.Z`（如 `v0.1.0`），不带 `go/` 之类的目录前缀；仓库里没有版本文件，也没有发布工作流。
- 使用方经 Go 模块代理按标签取用（`go get github.com/xihanfun/framework-sdk-go@vX.Y.Z`）；标签一经推送就被代理缓存，不能移动或复用，发错了只能用下一个号。
- 模块路径写在 `go.mod`；版本号由用户决定，不替用户预选版本级别或预发布后缀。

## 与规范标签的关系（RFC-0024「实现钉规范」）

- 发预发布或正式版时，`spec/` 子模块必须正好在规范的某个标签上，`go.mod` 里的一致性模块必须是同一提交上 `conformance/vX.Y.Z` 标签对应的版本，不能是伪版本。
- 正式版只能钉规范的正式版标签（不带预发布后缀），预发布可以钉 `vX.Y.Z-rc.N`。
- 规范还没有对应标签时，先在规范仓库打标签（同样要用户明确要求），再按 `AGENTS.md`「升规范指针」把子模块与 `go.mod` 指过去。
- 文档页「一致性」写明钉的规范、认领的档与缺口；成熟度到 Stable 需要认领的档没有缺口、运行器全部通过，且已有钉在规范正式版标签上的发布版。

## 步骤

每一步都在用户要求之后才做：

1. 工作区干净，在 `dev` 上；按 testing-release 技能跑全部门禁，CI 全绿。
2. 核对上一个标签到当前提交的公开 API 与行为变化，文档页的「破坏性改动与迁移」已写全（changelog-collect 技能）。
3. 核对 `spec/` 与 `go.mod` 的一致性模块都在规范标签上。
4. 用户明确说「推送并快进 main」时：`git fetch . dev:main`（不是快进就停下查），推送 `main`，等 CI 全绿。
5. 用户明确要求打标签时：`git tag -a vX.Y.Z -m "vX.Y.Z" main`，再 `git push origin vX.Y.Z`；推送用用户自己的权限。
6. 推送后核对 `go list -m github.com/xihanfun/framework-sdk-go@vX.Y.Z` 能解析。
