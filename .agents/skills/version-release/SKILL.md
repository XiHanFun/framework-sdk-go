---
name: xihan-sdk-go-version-release
description: 为 framework-sdk-go 准备版本、核对规范钉在标签上、打或推送版本标签时使用；适用于选定版本号之后的核对清单与 RFC-0024 的钉规范规则。只有用户明确要求时才打标签、推送或快进 main。
---

# framework-sdk-go 版本发布

## 版本真源

- 版本就是仓库的 git 标签：普通的 `vX.Y.Z`（如 `v0.1.0`），不带 `go/` 之类的目录前缀。`primitives/framework.go` 的 `SDKVersion` 是插件握手时上报的 SDK 版本，必须与标签相同（不带 `v`）。
- 使用方经 Go 模块代理按标签取用（`go get github.com/xihanfun/framework-sdk-go@vX.Y.Z`）；标签一经推送就对外可见、被代理缓存，不能移动或复用，发错了只能用下一个号。
- 模块路径写在 `go.mod`；版本号由用户决定，不替用户预选版本级别或预发布后缀。

## 发版核对与工作流

- `scripts/release-check.sh <标签> [<提交>]` 核对要打标签的那个提交（缺省为标签指向的提交，标签还没打时为 `HEAD`）：① 标签是 `v<语义化版本>`；② 提交在 `origin/main` 的历史里，预发布与正式版都只从 `main` 发；③ 标签号等于 `SDKVersion`；④ `spec/` 正好在规范标签上；⑤ `go.mod` 的一致性模块是同一提交上的 `conformance/` 标签版本。它要联网取 `origin/main` 与规范仓库的远端标签。
- 推送 `v*` 标签触发 `.github/workflows/release.yml`：guard 用同一份脚本复核，通过后让 `proxy.golang.org` 取一次这个版本（pkg.go.dev 随之收录）。工作流不建 GitHub Release，与 XiHan.Framework 相同，标签推送后手动建（见「步骤」）。
- 标签推上去版本就已发布，guard 拦不住，只能报红；所以打标签之前必须先在本地跑通 `release-check.sh`。guard 报红时不删标签（代理可能已经缓存），改正之后发下一个号。

## 与规范标签的关系（RFC-0024「实现钉规范」）

- 发预发布或正式版时，`spec/` 子模块必须正好在规范的某个标签上，`go.mod` 里的一致性模块必须是同一提交上 `conformance/vX.Y.Z` 标签对应的版本，不能是伪版本。
- 正式版只能钉规范的正式版标签（不带预发布后缀），预发布可以钉 `vX.Y.Z-rc.N`。
- 规范还没有对应标签时，先在规范仓库打标签（同样要用户明确要求），再按 `AGENTS.md`「升规范指针」把子模块与 `go.mod` 指过去。
- 文档页「一致性」写明钉的规范、认领的档与缺口；成熟度到 Stable 需要认领的档没有缺口、运行器全部通过，且已有钉在规范正式版标签上的发布版。

## 步骤

每一步都在用户要求之后才做：

1. 工作区干净，在 `dev` 上；按 testing-release 技能跑全部门禁，CI 全绿。
2. 核对上一个标签到当前提交的公开 API 与行为变化，文档页的「破坏性改动与迁移」已写全（changelog-collect 技能）。
3. 把 `SDKVersion` 改成这次的版本，`spec/` 与 `go.mod` 的一致性模块都指到规范标签，在 `dev` 上提交。
4. 用户明确说「推送并快进 main」时：`git fetch . dev:main`（不是快进就停下查），推送 `main`，等 CI 全绿。
5. 打标签之前跑 `bash scripts/release-check.sh vX.Y.Z main`，全部通过才往下走；不通过就停下报告。
6. 用户明确要求打标签时：`git tag -a vX.Y.Z -m "vX.Y.Z" main`，再 `git push origin vX.Y.Z`；推送用用户自己的权限。
7. 等 `release.yml` 跑完：guard 与模块代理收录两个任务都绿。
8. 用户要建 GitHub Release 时：`gh release create vX.Y.Z --verify-tag --title vX.Y.Z --notes-file <正文>`，预发布加 `--prerelease`，正文写钉的规范标签与这一版的变化，与 XiHan.Framework 相同。
