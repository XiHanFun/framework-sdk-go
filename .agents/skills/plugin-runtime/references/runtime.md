# 插件运行时：实现位置、平台差异与运行器

## 规范章节与实现文件

| 主题 | 规范 | 实现 | 测试 |
| --- | --- | --- | --- |
| 启动前核对清单与声明、读环境变量、选回调端点、监听、写握手行、stdin 与 SIGTERM | `handshake.md` §1–§2、`lifecycle.md` §7.7 | `plugin/serve.go` | `plugin/plugin_test.go`、`plugin/lifecycle_test.go`、`plugin/signal_unix_test.go` |
| 选项、生命周期回调、订阅方式 | RFC-0017、RFC-0009「订阅方式」 | `plugin/options.go` | `plugin/plugin_test.go`、`plugin/lifecycle_test.go` |
| `xihan.plugin.v1` 服务端、调用次序、健康推送、停止 | `lifecycle.md` §7.1–§7.5 | `plugin/server.go`、`plugin/state.go`、`plugin/calls.go` | `plugin/lifecycle_test.go`、`plugin/invocation_test.go` |
| 会话令牌 | `handshake.md` §4、ADR-0202 | `plugin/session.go` | `plugin/plugin_test.go` |
| 端点与监听 | `handshake.md` §2、§4 | `plugin/endpoint.go`、`endpoint_windows.go`、`endpoint_other.go` | `plugin/endpoint_windows_test.go`、`plugin/vectors_test.go`（`handshake-lines.json`、`host-addresses.json`） |
| 配置节的类型化读取 | `lifecycle.md` §7.2 | `plugin/configuration.go` | `plugin/configuration_test.go`、`plugin/lifecycle_test.go` |
| 上下文元数据、截止时间、错误的写出与读入 | `invocation.md` §2、§3 | `plugin/status.go` | `plugin/invocation_test.go`、`plugin/vectors_test.go`（`deadlines.json`、`error-reading.json`） |
| 宿主回调客户端 `Host` | `invocation.md` §5 | `plugin/host.go` | `plugin/callback_test.go`（含 `callback-status.json`） |
| JSON 日志 | `invocation.md` §6 | `plugin/log.go` | `plugin/plugin_test.go` |
| 消息目录与运行时自己的键 | `invocation.md` §8、RFC-0010 | `plugin/catalog.go`、`plugin/messages.go`、`plugin/Localization/` | `plugin/plugin_test.go`、`plugin/vectors_test.go`（`plugin-messages.json`） |
| 协议代码 | `plugin/plugin.proto` | `plugin/v1`（`buf generate`） | CI 的生成物检查 |

表里只给到文件；动手前在对应文件里核对函数名。

## 平台差异

- 监听：由 `plugin.Options` 的 `Network` / `Address` 选择（`plugin/endpoint.go` 的 `listen`）。缺省在所有平台都是 unix 套接字，放在新建的 `0700` 私有目录里；临时目录的路径含空白时改用 tcp，tcp 只监听 `127.0.0.1`。`npipe` 只在 Windows 上可用（go-winio），安全描述符只允许当前用户并拒绝远程客户端；其他平台上 `listenNamedPipe` 不可用。
- 回调端点：Windows 上能连 `npipe`、`unix`、`tcp`，其他系统能连 `unix`、`tcp`（`callbackNetworks` 分别在 `endpoint_windows.go` 与 `endpoint_other.go`）；从 `XIHAN_PLUGIN_HOST_ADDRESS` 的列表里取第一个能连的项，之后不换。
- 收尾：stdin 读到结尾在所有平台都是收尾请求；SIGTERM 只在 Unix 上有，测试在 `signal_unix_test.go`（`//go:build unix`）。命名管道的测试在 `endpoint_windows_test.go`（`//go:build windows`）。
- CI 在 Linux 上带 `-race`（需要 cgo），Windows 上不带。

## 运行器

- 版本：用 `go.mod` 里一致性模块的版本安装，`go list -m -f '{{.Version}}' github.com/xihanfun/framework-spec/conformance` 取得；示例的测试照这样装到临时 `GOBIN`。规范提交还没推送时，安装同样要按 `AGENTS.md`「升规范指针」的方式从本地克隆解析。
- 选项：`--dir` 插件目录、`--timeout` 每一步的截止时间、`--config` 下发的配置节 JSON、`--sample op=json` 操作的请求样例、`--exclude op` 不调用的操作、`--case name` 只跑某个用例、`--json` 输出 JSON、`--quiet` 不转发插件的 stderr。退出码 `0` 全部通过（含跳过）、`1` 有用例失败、`2` 参数或目录错误。
- 运行器把跳过也记为通过：示例的测试先看 `skipped`。`examples/echo` 不许任何跳过，排除会故意崩溃的 `echoes.crash`；`examples/cache` 只允许 `event` 跳过（没有订阅），名单写在 `expectedSkips`。
- 测试核对运行器跑的用例序列等于它的全集：从 `xihan-conformance help` 输出里以 `cases: ` 开头的一行读取，不直接引用一致性模块的 `plugin` 包（它与 `plugin/v1` 重复注册 `plugin.proto`，进同一个二进制就在初始化时 panic）。
- 运行器不提供宿主回调服务：回调的令牌、权限、本地核对与状态映射由本 SDK 的测试覆盖（回放 `callback-status.json`）。
