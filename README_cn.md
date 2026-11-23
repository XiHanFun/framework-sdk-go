<div align="center">

<h1>XiHan Framework SDK for Go</h1>

<h3>用 Go 写进程外插件与远程 Provider，与 .NET 宿主共用同一套一致性向量</h3>

<p>
  <a href="https://github.com/XiHanFun/framework-sdk-go/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/XiHanFun/framework-sdk-go/ci.yml?branch=main&style=flat-square&label=CI" /></a>
  <a href="https://pkg.go.dev/github.com/xihanfun/framework-sdk-go"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/xihanfun/framework-sdk-go.svg" /></a>
  <img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/XiHanFun/framework-sdk-go?style=flat-square&logo=go&logoColor=white&color=00ADD8" />
  <a href="./LICENSE"><img alt="MIT License" src="https://img.shields.io/github/license/XiHanFun/framework-sdk-go?style=flat-square&color=green" /></a>
  <a href="https://github.com/XiHanFun/framework-sdk-go/stargazers"><img alt="GitHub Stars" src="https://img.shields.io/github/stars/XiHanFun/framework-sdk-go?style=flat-square&logo=github&label=Stars&color=1f6feb" /></a>
</p>

<p>
  <a href="https://framework.docs.xihanfun.com/sdks/go"><b>文档</b></a> ·
  <a href="#快速开始"><b>快速开始</b></a> ·
  <a href="https://github.com/XiHanFun/framework-spec"><b>规范</b></a> ·
  <a href="https://github.com/XiHanFun/XiHan.Framework"><b>参考实现</b></a>
</p>

<p><a href="./README.md">English</a> | <b>简体中文</b></p>

</div>

[XiHan Spec](https://github.com/XiHanFun/framework-spec) 的 Go 实现：原语层、清单、调用上下文、管道、操作契约与进程外插件运行时，以及 `caching.distributed@1` 远程 Provider 的插件侧。用它可以写在 .NET 宿主（[XiHan.Framework](https://github.com/XiHanFun/XiHan.Framework)）里运行的 Go 插件，也可以把同一套原语用在 Go 应用里；规范以 submodule 放在 `spec/`，一致性由规范仓库的向量与黑盒运行器验证。

## 模块

| 包 | 内容 | 规范 |
| --- | --- | --- |
| `primitives` | `Error` 与错误码、`Result[T]`、`Maybe[T]`、`ID[Tag, V]`、生命周期、管道阶段、Baggage 键、消息键与模板 | RFC-0001、RFC-0010 |
| `manifest` | `xihan.manifest.json` 的模型、读取、规范化写出与校验 | RFC-0002 |
| `invocation` | 调用上下文：取消与截止时间、W3C 追踪、已登记的 Baggage | RFC-0003 |
| `pipeline` | 十一阶段的执行管道与排序规则 | RFC-0004 |
| `operation` | 操作契约、登记表与经完整管道执行的调用器 | RFC-0011 |
| `plugin` | 进程外插件运行时 `Serve` 与宿主回调客户端 `Host` | RFC-0017 |
| `plugin/v1` | 由 `spec/plugin/plugin.proto` 生成的协议代码 | `plugin/plugin.proto` |
| `caching` | `caching.distributed@1` 远程 Provider 的插件侧：存储契约、有界内存实现与操作集 | RFC-0019 |
| `examples/echo` | 回声插件示例，`cmd/echo-go` 是可执行入口 | — |
| `examples/cache` | 远程缓存 Provider 示例插件，`cmd/cache-go` 是可执行入口 | — |

## 安装

```bash
go get github.com/xihanfun/framework-sdk-go
```

## 快速开始

```go
package main

import (
	"context"
	_ "embed"
	"os"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/plugin"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

//go:embed xihan.manifest.json
var manifest []byte

type CreateRequest struct{ Text string `json:"text"` }
type CreateResponse struct{ Text, Tenant string }

func main() {
	registry := operation.NewRegistry()
	_ = operation.Add(registry, operation.Contract{ID: "echoes.create", Stability: primitives.Experimental, Expose: []string{operation.ExposeHTTP}},
		operation.HandlerFunc[CreateRequest, CreateResponse](func(ctx *invocation.Context, request CreateRequest) primitives.Result[CreateResponse] {
			return primitives.Ok(CreateResponse{Text: request.Text, Tenant: ctx.Tenant()})
		}), nil, "")

	if err := plugin.Serve(context.Background(), plugin.Options{Manifest: manifest, Operations: registry}); err != nil {
		os.Exit(1)
	}
}
```

清单 `xihan.manifest.json` 与构建出的可执行文件放在同一目录，宿主即可加载；用规范仓库的黑盒运行器 `xihan-conformance plugin --dir <产物目录>` 验证。清单、消息目录、订阅、回调与日志的写法见文档的[写一个插件](https://framework.docs.xihanfun.com/sdks/go#写一个插件)。

## 文档

| 去处 | 内容 |
| --- | --- |
| [Go SDK](https://framework.docs.xihanfun.com/sdks/go) | 模块、写一个插件、与宿主共用的行为、语言映射、一致性、破坏性改动与迁移 |
| [进程外插件](https://framework.docs.xihanfun.com/plugins/out-of-process) | 协议、握手与生命周期、宿主一侧的加载与回调、远程 Provider |
| [规范与多语言](https://framework.docs.xihanfun.com/appendix/spec) | 规范的组织、一致性档、各语言 SDK 的现状 |
| [XiHan Spec](https://github.com/XiHanFun/framework-spec) | RFC、术语表、协议映射、一致性向量与黑盒运行器 |

## 状态

认领的一致性档（RFC-0024）：`core`、`plugin` 与 `provider:caching.distributed@1`，没有缺口；钉的规范版本见 `spec/` 子模块与 `go.mod` 里的一致性模块。成熟度是 Experimental：已有钉在规范 rc 标签上的预发布版，到 Stable 还要钉在规范正式版标签上的发布版；流式操作与 Wasm 载体不在范围内。细节见文档的[一致性](https://framework.docs.xihanfun.com/sdks/go#一致性)。

## 生态

| 项目 | 说明 |
| --- | --- |
| [framework-spec](https://github.com/XiHanFun/framework-spec) | XiHan Spec：RFC、术语表、协议映射与一致性向量 |
| [XiHan.Framework](https://github.com/XiHanFun/XiHan.Framework) | .NET 参考实现 |
| [framework-sdk-typescript](https://github.com/XiHanFun/framework-sdk-typescript) | TypeScript SDK |
| [framework-sdk-rust](https://github.com/XiHanFun/framework-sdk-rust) | Rust SDK |
| [framework-sdk-java](https://github.com/XiHanFun/framework-sdk-java) | Java SDK |
| [framework-sdk-python](https://github.com/XiHanFun/framework-sdk-python) | Python SDK |
| [framework-sdk-php](https://github.com/XiHanFun/framework-sdk-php) | PHP SDK |

## 参与

欢迎提交 [Issue](https://github.com/XiHanFun/framework-sdk-go/issues) 与 Pull Request。公开契约的变更不在 SDK 仓库里做，先走 framework-spec 的 RFC 流程，再同步到各 SDK；开发约定、门禁与发布流程见 [AGENTS.md](./AGENTS.md)。

## 许可证

[MIT](./LICENSE) © 2021-Present XiHanFun and contributors。
