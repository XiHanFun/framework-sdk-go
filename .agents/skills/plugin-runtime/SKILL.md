---
name: xihan-sdk-go-plugin-runtime
description: 实现、修复或审查 framework-sdk-go 的进程外插件运行时时使用，适用于 plugin 包、plugin/v1 协议代码的生成、examples/echo、握手、生命周期、调用、回调与黑盒运行器的用法和平台差异；原语与操作契约用 sdk-development，缓存操作集用 remote-provider。
---

# framework-sdk-go 插件运行时

规范章节与实现文件的对照、平台差异和运行器的细节读 `references/runtime.md`。

## 规范位置

- RFC-0017《进程外插件协议》。
- `spec/plugin/handshake.md`：环境变量、监听与握手行、版本协商、会话令牌、回调端点列表。
- `spec/plugin/lifecycle.md` §7：调用次序、配置、就绪与健康、停止与收尾的时限。
- `spec/plugin/invocation.md`：上下文元数据与截止时间、错误的写出与读入、事件投递、回调、日志、运行时的消息键（§8）。
- `spec/plugin/plugin.proto`：协议；向量在 `spec/conformance/vectors/plugin/`，运行器用例表在 `spec/conformance/README.md`。

## 做法

1. 先读规范的对应小节与向量，再读 `plugin/` 里对应的文件和测试；.NET 宿主一侧在 `../XiHan.Framework/framework/src/XiHan.Framework.Plugins.OutOfProcess`，协议常量与运行时目录在 `XiHan.Framework.Plugins.Protocol`。
2. 规范的判定（握手行、端点、截止时间、回调状态映射、读入错误）调用一致性模块的 `rules`，不另写。
3. 改了 `spec/plugin/plugin.proto` 或升了规范指针就 `buf generate`，`plugin/v1` 不手改；CI 以 `git status --porcelain -- plugin/v1` 判断生成物与规范一致。
4. 运行时自己的消息键在 `plugin/messages.go` 与 `plugin/Localization/`（10 种区域，译文与 .NET 相同），不随握手发送。
5. 改完跑 `go test ./plugin/... ./examples/...`；`examples/echo` 的测试用黑盒运行器跑全部用例，任何跳过都算失败。

## 黑盒运行器

```bash
go install github.com/xihanfun/framework-spec/conformance/cmd/xihan-conformance@<go.mod 里一致性模块的版本>
xihan-conformance plugin --dir <插件目录> --exclude echoes.crash --sample 'echoes.create={"text":"x"}'
```

插件目录里要有 `xihan.manifest.json` 与 `entry` 指向的可执行文件。运行器自带一份协议生成代码，与本 SDK 的 `plugin/v1` 注册同一个 `plugin.proto`，不能链接进同一个二进制，所以测试以独立进程运行它，用例全集从它的 `help` 输出里 `cases: ` 一行读取。

## 完成条件

- 规范要求的行为都有测试，向量回放写进 `vectors_test.go`。
- `examples/echo` 与 `examples/cache` 的运行器测试在 Linux 与 Windows 上都通过（CI 矩阵）。
- 行为或公开 API 变化同步到 XiHan.Framework 的 `docs/sdks/go.md`「插件运行时」与「破坏性改动与迁移」。
