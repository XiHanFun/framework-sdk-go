# 门禁、运行器测试与本机环境

## CI 门禁（`.github/workflows/ci.yml`）

| 步骤 | 内容 | 失败时 |
| --- | --- | --- |
| 检出 | 带 submodule（`submodules: true`） | 本地先 `git submodule update --init` |
| 安装 Go | `go-version-file: go.mod`（指令带补丁号如 `1.26.0` 时装的就是那一版；升依赖让 `go mod tidy` 改了 `go` 指令时，CI 的工具链随之变） | 本机 Go 低于 `go.mod` 的 `go` 指令时升级 |
| 安装 buf 与生成器（只在 `ubuntu-latest`） | 三条 `go install`，版本写在 ci.yml；这一步与 staticcheck 设 `GOTOOLCHAIN=auto`，工具按各自要求的工具链编译 | 本机照同样的版本安装；工具要求的 Go 高于 `go` 指令时不要为它去抬 `go` 指令 |
| 生成物与规范一致（只在 `ubuntu-latest`） | `buf generate` 后 `git status --porcelain -- plugin/v1` 必须为空（改动、删除与新增的未跟踪文件都算） | 提交重新生成的 `plugin/v1`，不手改 |
| 格式与静态检查 | `test -z "$(gofmt -l .)"`、`go vet ./...` | `gofmt -w <文件>`；按 vet 的提示改代码 |
| staticcheck（只在 `ubuntu-latest`） | `go install` 钉死的版本后 `staticcheck ./...` | 按提示改代码（未使用的代码删掉，不加忽略指令绕过）；本机照 ci.yml 的版本安装 |
| 测试 | `go test ./...`，Linux 上加 `-race`（需要 cgo），Windows 上不带 | 修代码或补测试 |
| 依赖漏洞扫描（单独的作业，最新稳定工具链） | `go install` 钉死的版本后 `govulncheck ./...` | 升到修复了漏洞的依赖版本（`go get <模块>@<版本>` 后 `go mod tidy`），再跑全部测试；不加忽略 |

矩阵是 `ubuntu-latest` 与 `windows-latest`；生成物只取决于 `plugin.proto` 与生成器的版本，安装 buf 与生成物检查只在 `ubuntu-latest` 上跑，Windows 任务不编译 buf；staticcheck 也只在 `ubuntu-latest` 上跑。触发条件是 `main`、`dev` 的 push，目标为 `main`、`dev` 的 PR，以及手动触发。

## 运行器测试

`examples/echo/echo_test.go` 与 `examples/cache/cache_test.go`：

1. `go build` 插件到临时目录，写入同一份清单。
2. `go list -m -f '{{.Version}}' github.com/xihanfun/framework-spec/conformance` 取 `go.mod` 的版本，`go install` 运行器到临时 `GOBIN`。
3. 以独立进程运行 `xihan-conformance plugin --dir <目录> --json --quiet`，带 `--config`、`--sample`、`--exclude`。
4. 核对用例序列等于 `xihan-conformance help` 输出里 `cases: ` 一行的全集；跳过只允许 `expectedSkips` 里的（echo 没有，cache 只有 `event`）。

这两个测试需要网络或模块缓存里有运行器的源码；`go` 不在 PATH 上时跳过。

## 本机环境

- Go 不在 shell 的 PATH 上（Windows 缺省装在 `C:\Program Files\Go`）时，Bash 里命令前加 `export PATH="/c/Program Files/Go/bin:$HOME/go/bin:$PATH"`；`$HOME/go/bin` 是 `go install` 的 buf 与生成器所在。
- 国内拉依赖：`GOPROXY="https://goproxy.cn,direct" GOSUMDB="sum.golang.google.cn"`；跑 `examples/echo`、`examples/cache` 的测试另加 `GOPRIVATE=github.com/xihanfun/framework-spec`。
- 规范提交还没推送、`go.mod` 指向它的伪版本时，按 `AGENTS.md`「升规范指针」只在当前命令的环境里从本地克隆解析。
- `-race` 需要 cgo 与 C 编译器；Windows 上没有时不带 `-race`，由 CI 的 Linux 任务覆盖。只在 Unix 上编译的测试（`plugin/signal_unix_test.go`）同样由 Linux 任务覆盖；要在本机跑，用 WSL 或 Docker 官方镜像 `golang`（标签与 `go.mod` 的 `go` 指令一致）挂载仓库执行 `go test -race ./...`。
