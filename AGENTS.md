# AGENTS.md

本文件约束在 framework-sdk-go 仓库内工作的 AI Agent。回答、文档与提交说明使用中文；代码标识、公开 API 和标准技术术语保持英文。

## 仓库概览

framework-sdk-go 是 [XiHan Spec](https://github.com/XiHanFun/framework-spec) 的 Go 实现，模块路径 `github.com/xihanfun/framework-sdk-go`：原语层、清单、调用上下文、管道、操作契约、进程外插件运行时，以及 `caching.distributed@1` 远程 Provider 的插件侧。用它写的插件在 .NET 宿主（[XiHan.Framework](https://github.com/XiHanFun/XiHan.Framework)，参考实现）里运行；规范以 submodule 放在 `spec/`，一致性由规范仓库的向量、档表与黑盒运行器验证。

### 技术栈

| 技术 | 要求 | 用途 |
| --- | --- | --- |
| Go | `go.mod` 的 `go` 指令（CI 以 `go-version-file: go.mod` 安装） | 编译、测试与运行器 |
| gRPC 与 protobuf | `google.golang.org/grpc`、`google.golang.org/protobuf`，以 `go.mod` 为准 | 插件协议 `xihan.plugin.v1` |
| go-winio | `github.com/Microsoft/go-winio`，以 `go.mod` 为准 | Windows 命名管道的监听与回调 |
| 一致性模块 | `github.com/xihanfun/framework-spec/conformance`，`go.mod` 钉的版本与 `spec/` 子模块是同一个规范提交 | 内嵌的向量与档表、规范规则 `rules`、黑盒运行器 `xihan-conformance` |
| buf、protoc-gen-go、protoc-gen-go-grpc | 版本以 `.github/workflows/ci.yml` 为准 | 从 `spec/plugin/plugin.proto` 生成 `plugin/v1` |
| gofmt、go vet | Go 工具链自带 | 格式与静态检查 |
| staticcheck | 版本以 `.github/workflows/ci.yml` 为准 | 静态检查（未使用的代码、可疑写法等） |
| govulncheck | 版本以 `.github/workflows/ci.yml` 为准 | 依赖漏洞扫描：只在代码调得到漏洞时失败，CI 用最新稳定工具链单独一个作业跑 |
| GitHub Actions | `ubuntu-latest` 与 `windows-latest` 矩阵；安装 buf、生成物检查与 staticcheck 只在 `ubuntu-latest` 上跑 | CI 门禁 |

## 开始前必须做

1. 按任务读取对应技能：
   - 原语、清单、调用上下文、管道、操作契约的设计、实现与对齐读取 `.agents/skills/sdk-development/SKILL.md`。
   - `caching` 包与 `examples/cache`（远程 Provider）读取 `.agents/skills/remote-provider/SKILL.md`。
   - 进程外插件运行时（`plugin/`、`plugin/v1`、`examples/echo`）、协议代码生成与黑盒运行器读取 `.agents/skills/plugin-runtime/SKILL.md`。
   - 门禁、向量覆盖测试与本机验证读取 `.agents/skills/testing-release/SKILL.md`；版本与标签读取 `.agents/skills/version-release/SKILL.md`。
   - 整理用户可感知变化读取 `.agents/skills/changelog-collect/SKILL.md`；生成提交信息读取 `.agents/skills/commit-msg/SKILL.md`；准备 PR 读取 `.agents/skills/create-pr/SKILL.md`；处理 Issue 读取 `.agents/skills/issue-reply/SKILL.md`；审查测试读取 `.agents/skills/test-review/SKILL.md`。
2. 同时涉及多个职责时加载对应多个技能，不要一次读取无关资料。
3. 检查当前分支、`git status` 和最近提交；保留用户已有改动。
4. 读相关规范章节：`spec/rfcs/<编号>-*.md`（原语 RFC-0001、清单 RFC-0002、调用上下文 RFC-0003、管道 RFC-0004、本地化 RFC-0010、操作 RFC-0011、插件协议 RFC-0017、远程 Provider RFC-0019、版本与一致性档 RFC-0024）、`spec/conventions/`、`spec/manifest/manifest.schema.json`、`spec/plugin/handshake.md` / `lifecycle.md` / `invocation.md` / `plugin.proto`、`spec/provider/caching-distributed.md`、`spec/conformance/profiles.json` 与 `spec/conformance/vectors/` 下对应的向量。
5. 读目标包的源文件与 `*_test.go`、仓库根的 `vectors_test.go`，以及 .NET 参考实现里对应的类型（本地克隆 `../XiHan.Framework/framework/src`）。
6. 先找已有实现：JSON 文本的共同检查在 `internal/jsontext`，载荷解码与值模型在 `internal/jsoncodec`；规范的文法与判定先看一致性模块的 `rules`，SDK 已经调用的（空白字符集、操作 ID、主题、协议版本协商、握手行与端点、截止时间、回调的权限与状态映射、读入错误）直接复用，不另写一份。原语层、清单、traceparent、管道排序与 JSON 文本检查是 SDK 自己实现、靠回放向量验证的，两类各有哪些见 `sdk-development` 技能的 `references/spec-map.md`。

## 目录结构

```text
/
├─ AGENTS.md
├─ .agents/skills/                     # 按开发、插件运行时、远程 Provider、测试与发布分类的仓库技能
├─ .claude/skills -> ../.agents/skills # Claude Code 技能兼容入口
├─ CLAUDE.md -> AGENTS.md              # Claude Code 仓库指令入口
├─ .github/workflows/ci.yml            # 门禁：生成物与规范一致（只在 ubuntu 上）、格式与静态检查（gofmt、go vet、staticcheck）、全部测试
├─ .github/workflows/release.yml       # 发布：推送 v* 标签触发，核对后让模块代理收录
├─ scripts/release-check.sh            # 发版核对：打标签之前与 release.yml 的 guard 都跑它
├─ spec/                               # 规范（XiHanFun/framework-spec 的 submodule）：RFC、约定、Schema、插件协议、向量与运行器
├─ buf.gen.yaml                        # 从 spec/plugin/plugin.proto 生成 plugin/v1
├─ go.mod / go.sum                     # 模块定义；钉一致性模块的版本
├─ vectors_test.go                     # 按一致性档核对向量的回放（RFC-0024）
├─ primitives/                         # Error、Result、Maybe、ID、生命周期、管道阶段、Baggage 键、消息键与模板
├─ manifest/                           # 清单模型、读取、规范化写出与校验
├─ invocation/                         # 调用上下文：取消与截止时间、追踪、Baggage、对象袋
├─ pipeline/                           # 十一阶段的执行管道
├─ operation/                          # 操作契约、登记表与经完整管道执行的调用器
├─ plugin/                             # 进程外插件运行时；Localization/ 是运行时自己的 10 种区域
│  └─ v1/                              # buf generate 生成的协议代码，不手改
├─ caching/                            # caching.distributed@1 远程 Provider 的插件侧
├─ internal/
│  ├─ jsontext/                        # 读取 JSON 文本的共同检查（RFC-0001「JSON 文本」）
│  └─ jsoncodec/                       # SDK 读写 JSON 值的共用实现
└─ examples/
   ├─ echo/                            # 回声插件；cmd/echo-go 是可执行入口
   └─ cache/                           # 远程缓存 Provider 插件；cmd/cache-go 是可执行入口
```

`examples/` 与 SDK 同属一个 Go 模块，没有单独的 `go.mod`。

## 常用命令

在仓库根执行：

| 任务 | 命令 |
| --- | --- |
| 初始化规范 | `git submodule update --init` |
| 安装生成器 | 照 `ci.yml`「安装 buf 与生成器」一步的三条 `go install`（版本以 ci.yml 为准） |
| 生成协议代码 | `buf generate`（改了 `spec/plugin/plugin.proto` 或升了规范指针之后） |
| 生成物检查 | `buf generate` 后 `git status --porcelain -- plugin/v1` 输出为空 |
| 格式 | `gofmt -l .` 输出为空 |
| 静态检查 | `go vet ./...` 与 `staticcheck ./...`（照 `ci.yml`「staticcheck」一步的版本 `go install`） |
| 测试 | `go test ./...`（Linux 上 CI 加 `-race`，它需要 cgo） |
| 依赖漏洞扫描 | `govulncheck ./...`（照 `ci.yml`「govulncheck」一步的版本 `go install`；升依赖之后必跑。本机工具链不是最新补丁时会报标准库的漏洞，那些随工具链升级修复，要看的是依赖里的） |
| 全部门禁 | `buf generate && test -z "$(git status --porcelain -- plugin/v1)" && test -z "$(gofmt -l .)" && go vet ./... && staticcheck ./... && go test ./...` |
| 安装黑盒运行器 | `go install github.com/xihanfun/framework-spec/conformance/cmd/xihan-conformance@<go.mod 里一致性模块的版本>` |
| 黑盒运行器 | `xihan-conformance plugin --dir <插件目录> --exclude echoes.crash --sample 'echoes.create={"text":"x"}'` |
| 发版核对 | `bash scripts/release-check.sh vX.Y.Z main`（打标签之前跑，联网取 `origin/main` 与规范的远端标签） |

国内拉依赖时在命令前加 `GOPROXY="https://goproxy.cn,direct" GOSUMDB="sum.golang.google.cn"`；`examples/echo`、`examples/cache` 的测试会按 `go.mod` 的版本 `go install` 运行器，另加 `GOPRIVATE=github.com/xihanfun/framework-spec`。

## 规范与一致性

- 规范是事实源：以 `spec/` 子模块钉的提交为准，`go.mod` 里的一致性模块指向同一个提交。`spec/` 供阅读规范与 `buf generate` 使用；测试读的向量与档表是一致性模块内嵌的那一份（`conformance.Vectors`、`conformance.Profiles()` 与类型化入口），不经 submodule 读。
- .NET（XiHan.Framework）是参考实现，行为与它一致：向量的期望值由 .NET 算出；内核错误的码、键与参数与 .NET 相同，en-US 默认模板逐字照 .NET 的语言文件；插件运行时的 10 种区域译文与 `XiHan.Framework.Plugins.Protocol` 的语言文件相同。不随 `encoding/json` 等 Go 库的习惯。
- 向量回放与一致性档（RFC-0024）：本 SDK 认领 `core`、`plugin` 与 `provider:caching.distributed@1`。`vectors_test.go` 按 `conformance.Profiles()` 的档表核对，认领的档要求的每个向量要么被测试回放（列在 `replayedVectors`，注明回放它的测试），要么列进 `gaps` 并写明理由；规范在认领的档里新增向量时这个测试失败。
- 规范没写到的行为不自行发明：停下来向用户报告差异，提出规范文字，在 framework-spec 提交（本地克隆 `../framework-spec`）。只属于 Go 的选择（规范不规定的 Go 类型怎样处理）记为 XiHan.Framework 的 ADR 补充，并写进文档页，前例是 ADR-0236 的补充。
- 公开契约的变化顺序：framework-spec 的 RFC → XiHan.Framework 的 ADR（`plans/adr`）→ 各 SDK。提交说明写明对应的 ADR 编号。

### 升规范指针

1. 规范的改动先在 framework-spec 提交。
2. 子模块指到新提交：`git -C spec fetch origin && git -C spec checkout <哈希>`；规范提交还没推送时从本地克隆取：`git -C spec fetch "$PWD/../framework-spec" dev && git -C spec checkout <哈希>`。
3. 一致性模块升到同一提交的伪版本：`go get github.com/xihanfun/framework-spec/conformance@<哈希>`。规范提交还没推送时，模块代理解析不到它，只在这一条命令的环境里从本地克隆解析，不写进 git 配置：

   ```bash
   GOPRIVATE=github.com/xihanfun/framework-spec \
   GIT_CONFIG_COUNT=2 \
   GIT_CONFIG_KEY_0="url.file:///E:/Repository/XiHanFun/framework-spec.insteadOf" \
   GIT_CONFIG_VALUE_0="https://github.com/xihanfun/framework-spec" \
   GIT_CONFIG_KEY_1="protocol.file.allow" GIT_CONFIG_VALUE_1="always" \
   go get github.com/xihanfun/framework-spec/conformance@<哈希>
   ```

   `file:///` 后面是本机克隆的绝对路径，换机器时照实改。推送本仓库之前，规范的提交必须已推送，否则 CI 解析不到这个伪版本。
4. 协议有变化时 `buf generate`；跑全部门禁，`vectors_test.go` 指出的新向量要么回放、要么列为缺口。
5. 子模块指针、`go.mod` 与 `go.sum` 一起提交，或与随规范改动的实现同一提交，说明里写「规范指针与 go.mod 的一致性模块升到 <短哈希>」。

## 代码规范

- 格式与静态检查是 `gofmt`、`go vet` 与 `staticcheck`（CI 必过；staticcheck 只在 ubuntu 上跑，改了 `*_windows.go` 在本机跑一遍），没有另外的 lint 工具；`.editorconfig` 规定 UTF-8、LF、末尾换行，Go 用 tab，其余缩进两个空格；`.gitattributes` 统一 LF，`*.pb.go` 标为生成物。
- 文档注释与 `//` 注释用中文：导出与未导出的标识符都写文档注释，以标识符开头，一句说清它是什么，常在括号里注明对应的规范章节与 ADR（如「（RFC-0001「空白字符」，ADR-0245）」）；方法内的 `//` 注释说明为什么（判定顺序、平台差异、并发与时限），不复述代码。
- 运行时英文：`FrameworkError` 的 `Message`、`reason` 的固定文本、测试的断言与失败信息用英文，句子以句号结尾，标识符用单引号。
- 可恢复的失败返回 `*primitives.Error` 或 `primitives.Result[T]`（`primitives.Fail`、`Validation`、`NotFound` 等工厂），不 panic；编程错误（错误码、消息键、ID 不合法，管道重复登记等）panic `*primitives.FrameworkError`。错误码按 `<领域大写>_<KIND>_<NNN>`，登记在 `spec/conventions/error-codes.md`。
- 文案不写在代码里：错误只带消息键与参数（`primitives.NewError(code, key, details, inner)`），`primitives.Literal` 只用于线上还原；目录是 `Localization/<区域>.json`（键 → 模板，至少 `en-US.json`），经 `plugin.LoadCatalog` 加载。运行时自己的目录（`plugin/Localization/`）带齐 10 种区域。
- 取消与截止时间经 `invocation.Context`（嵌入 `context.Context`）一路传下去；处理器在调用方的 goroutine 里同步执行，不另起 goroutine 放弃它；超时与取消的归因只看截止令牌（`Context.Cancellation()`）。
- 读 JSON 先过 `internal/jsontext` 的共同检查再解码，成员名区分大小写、重复成员拒绝；不直接交给 `encoding/json`。
- 规范的文法与判定：`rules` 里有、SDK 已经在调用的（`rules.IsWhitespace`、`rules.IsOperationID`、`rules.IsTopic`、`rules.NegotiateVersion`、`rules.ParseEndpoint`、`rules.ParseDeadline`、`rules.MapCallbackStatus`、`rules.ReadProtoError` 等）一律调用，不另写一份，免得与规范的表悄悄分叉；新增的判定先看 `rules` 有没有。原语层（错误码、消息键与模板、Baggage 键与值、生命周期）、`invocation` 的 traceparent、`manifest` 的清单文法、`pipeline` 的排序与 `internal/jsontext` 的 JSON 文本检查是自己实现的，各自回放对应的向量，改它们时以向量为准。清单见 `sdk-development` 技能的 `references/spec-map.md`。
- 不建无界集合：登记表与存储都有容量（`operation.Capacity`、`caching.DefaultCapacity` 与 `caching.WithCapacity`），满了返回错误。
- 平台差异放在 `_windows.go` / `_other.go` 文件里（`//go:build windows` / `!windows`）；`plugin/v1` 只由 `buf generate` 生成。

## 测试

- 修改哪个包，就在那个包添加或更新 `*_test.go`；优先验证公开可观察行为、错误码与消息键、取消与截止时间、边界与容量。
- 向量回放：回放测试读一致性模块的类型化入口，命名如 `TestXxxFollowsTheVectors`，新回放的文件加进 `vectors_test.go` 的 `replayedVectors` 并注明测试名。
- 插件运行时用黑盒运行器跑全部用例：`examples/echo`、`examples/cache` 的测试构建插件、按 `go.mod` 的版本安装运行器并以独立进程运行它，用例序列必须等于运行器的全集，名单外的跳过算失败；CI 在 Linux 与 Windows 上都跑。
- 修复缺陷先写能复现问题的测试，再改实现。
- 不删除测试、不放宽门禁（生成物检查、`gofmt`、`go vet`、`staticcheck`、`-race`、向量覆盖）来通过。

## 文档

- README 只放概览、安装、一个最小示例与链接，`README.md`（英文）与 `README_cn.md`（中文）两份同步。
- 详细文档在 XiHan.Framework 仓库的 `docs/sdks/go.md`（站点 `https://framework.docs.xihanfun.com/sdks/go`），本地克隆在 `../XiHan.Framework`。那是另一个仓库，单独提交；构建在那个仓库根执行 `pnpm --dir docs build`。
- 文档正文不写版本号（SDK、规范与依赖的都不写），用「见 `go.mod`」「见 `spec/` 子模块」指向真源。
- 改公开 API 或可观察行为时，同步文档页的对应章节与其中的「破坏性改动与迁移」。

## 发布

- 版本就是仓库的 git 标签：普通的 `vX.Y.Z` 标签，不带 `go/` 之类的目录前缀；模块路径在 `go.mod`，`primitives/framework.go` 的 `SDKVersion`（握手上报的 SDK 版本）要与标签相同。使用方经 Go 模块代理按标签取用，没有另外的注册表。
- 打标签之前跑 `bash scripts/release-check.sh vX.Y.Z main`（标签只从 `main` 发、标签号等于 `SDKVersion`、规范钉在标签上、一致性模块是标签版本）；推送 `v*` 标签触发 `.github/workflows/release.yml`，guard 用同一份脚本复核，通过后让模块代理收录；GitHub Release 与 XiHan.Framework 一样在标签推送后手动建。标签推上去就已发布，guard 只能报红、拦不住，步骤见 `version-release` 技能。
- 规范的标签规则照 RFC-0024「实现钉规范」：发预发布或正式版时 `spec/` 子模块必须正好在规范标签上，`go.mod` 里的一致性模块必须是标签版本（规范同一提交上的 `conformance/vX.Y.Z` 标签），不能是伪版本；正式版只钉规范的正式版标签，预发布可以钉 rc 标签。
- 打标签与推送标签用用户自己的权限，只在用户明确要求时执行。

## 分支

- 日常提交在 `dev`。
- `main` 只在用户明确说「推送并快进 main」时由 `dev` 快进（`git fetch . dev:main`，不是快进就停下查）。
- CI（`.github/workflows/ci.yml`）在 `main` 与 `dev` 的 push、目标为 `main` 或 `dev` 的 PR 上运行，也可以手动触发（`workflow_dispatch`）。
- 发版工作流（`.github/workflows/release.yml`）只在推送 `v*` 标签、或手动运行时选中版本标签时运行，见「发布」与 `version-release` 技能。

## Git 与提交

- Conventional Commits：`<type>(<scope>): <中文说明>`，scope 用本仓库的包名（`primitives`、`manifest`、`invocation`、`pipeline`、`operation`、`plugin`、`caching`、`examples`、`conformance`）或 `spec`、`docs`、`build`、`ci`。
- 保留他人已有和无关的改动；只暂存自己的文件，不 reset、checkout 或清理别人的改动。
- 提交前检查 `git diff --check` 与相关门禁（至少 `gofmt -l .`、`go vet ./...` 与受影响包的测试）。
- 不推送、不打标签、不发布，除非用户明确要求。

## 禁止事项

- 不在规范之外发明行为，不为迁就某个宿主或 Go 库的习惯偏离规范与 .NET 参考实现。
- 不手改 `plugin/v1` 等生成物，不手改 `spec/` 子模块里的文件代替在 framework-spec 提交。
- 不添加静默兜底、无限重试或无界内存集合。
- 从本地克隆解析伪版本用的 `GIT_CONFIG_*` 与 `GOPRIVATE` 只设在当前命令的环境里，不写进 git 配置或 `go env`。
- 不删测试、不跳过门禁、不把名单外的运行器跳过当作通过。
- 不猜测 API、版本或 CI 命令；以当前检出为准。
