---
name: xihan-sdk-go-testing-release
description: 运行或排查 framework-sdk-go 的门禁、向量覆盖测试、黑盒运行器测试与本机验证时使用，适用于对照 ci.yml 复现 CI、维护 vectors_test.go、处理本机缺工具链或国内拉依赖；不负责实现功能，打标签见 version-release。
---

# framework-sdk-go 测试与门禁

先读 `references/gates.md`，再以当前 `.github/workflows/ci.yml`、`go.mod` 与 `vectors_test.go` 为事实源。

## 标准验证

在仓库根执行，顺序与 CI 相同：

```bash
git submodule update --init
buf generate
test -z "$(git status --porcelain -- plugin/v1)"
test -z "$(gofmt -l .)"
go vet ./...
staticcheck ./...        # CI 只在 ubuntu 上跑
go test ./...            # Linux 上 CI 是 go test -race ./...
```

- 生成器 buf、protoc-gen-go、protoc-gen-go-grpc 按 ci.yml「安装 buf 与生成器」一步的版本 `go install`；版本不同，生成物可能对不上。
- staticcheck 按 ci.yml「staticcheck」一步的版本 `go install`；版本不同，检查项不同。CI 只分析 Linux 的构建，改了 `*_windows.go` 在 Windows 本机跑一遍。
- 先跑受影响的包（`go test ./plugin/...`），再跑全部。
- 不删测试、不放宽门禁、不把运行器名单外的跳过当作通过来制造绿色结果。

## 向量覆盖

仓库根的 `vectors_test.go` 按 `go.mod` 钉住的一致性模块内嵌的档表核对认领的档（RFC-0024）。它失败时按信息处理：

- 认领的档要求、却既没回放也没列为缺口的向量：写回放测试并加进 `replayedVectors`（注明测试名），确实做不到时列进 `gaps` 写理由。
- 缺口已被回放、缺口不在认领的档里、回放的文件规范里没有、认领的档规范里没有定义：改 `replayedVectors`、`gaps` 或 `claimedProfiles`，不改测试逻辑。

## 打包与发布

Go 模块没有打包步骤，使用方按标签从模块代理取用；CI 也没有打包检查。打标签前的核对见 version-release 技能。
