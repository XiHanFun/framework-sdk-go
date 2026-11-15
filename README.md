# XiHan Framework SDK for Go

[XiHan Framework Spec](https://github.com/XiHanFun/framework-spec) 的 Go 实现：原语层、清单、调用上下文、管道、操作契约与进程外插件运行时。用它可以写在 .NET 宿主（XiHan.Framework）里运行的 Go 插件，也可以把同一套原语用在 Go 应用里。规范以 submodule 放在 `spec/`，一致性由规范仓库的向量与黑盒运行器验证。

```bash
go get github.com/xihanfun/framework-sdk-go
```

| 包 | 内容 | 规范 |
| --- | --- | --- |
| `primitives` | `Error` / 错误码、`Result[T]`、`Maybe[T]`、`ID[Tag, V]`、`LifecycleState`、`PipelineStage`、`Stability`、Baggage 键、消息键与模板、`FrameworkError` | RFC-0001、RFC-0010、`conventions/` |
| `manifest` | `xihan.manifest.json` 的模型、读取、规范化输出与校验，能力声明与依赖文本 | RFC-0002、`manifest/manifest.schema.json` |
| `invocation` | `Context`：取消与截止时间（嵌入 `context.Context`）、W3C 追踪、已登记的 Baggage、对象袋；超时与取消翻译为 `KERNEL_TIMEOUT_001` / `KERNEL_CANCELLED_001` | RFC-0003 |
| `pipeline` | 十一阶段的 `Builder`：`Use` / `UseAt` / `UseNamedInstance`、`Before` / `After`、`Describe` / `Dump`、`Build`；排序规则与错误码 `KERNEL_PIPELINE_00x` | RFC-0004 |
| `operation` | `Contract`、`Schema`、`Handler[Req, Res]`、`Registry`、经完整管道执行的 `Invoker`（XGI：JSON 请求与响应） | RFC-0011 |
| `plugin` | 进程外插件运行时 `Serve`：环境变量、监听（unix 套接字在私有目录里 / tcp / npipe）、握手行、`xihan.plugin.v1` 服务端（每次调用核对宿主的会话令牌）、生命周期状态机与健康推送、事件投递、宿主回调客户端 `Host`、宿主可解析的 JSON 日志、消息目录 | RFC-0017、`plugin/` |
| `plugin/v1` | 由 `spec/plugin/plugin.proto` 生成的协议代码（`buf generate`） | `plugin/plugin.proto` |
| `examples/echo` | 回声插件：操作、订阅、健康、配置、宿主回调；`cmd/echo-go` 是可执行入口 | — |

## 写一个插件

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

1. 清单 `xihan.manifest.json`：`kind: plugin`、`isolation: out-of-process`、`contributes.operations` 列出全部操作、`entry` 是可执行文件名；不写 `contributes.pipeline`（只有 Wasm 插件能贡献中间件，`manifest.Read` 读到非空即拒绝，与宿主一致）；构建产物目录里放同一份文件，宿主读它、运行时握手时原样发送。
2. 操作用 `operation.Add` 登记，请求与响应是普通的 JSON 结构；可恢复失败返回 `primitives.Fail`，错误码按 `<领域大写>_<KIND>_<NNN>`，文案用消息键 + `plugin.LoadCatalog` 加载的 `Localization/<区域>.json`。
3. 订阅、生命周期回调、健康项经 `plugin.Options` 接入；`Configure` 回调里用 `configuration.Decode(&settings)` 读配置节（宿主以字符串下发数字与布尔，按字段类型转换）；操作里用 `plugin.HostOf(ctx)` 回调宿主（`Invoke` 经宿主完整管道，`Publish` 经宿主消息代理），权限由清单 `permissions` 声明、宿主授予。订阅的 `Mode` 选订阅方式（RFC-0009「订阅方式」）：空或 `plugin.SubscriptionModeCompeting` 是竞争消费，订阅了主题的宿主实例里只有一个把消息交给插件；`plugin.SubscriptionModeBroadcast` 是广播，每个在线的宿主实例都交给自己的插件实例，尽力而为、不进死信，适合清本地缓存这类每个实例都要做的事。同一（主题，类型）对只能声明一次，不能以两种方式各声明一次，`Serve` 启动时即拒绝；同一主题的不同类型可以各用一种方式，宿主投递时带上这次的方式，运行时只交给以这种方式声明的处理器。
4. 日志写 stderr：`slog.New(plugin.NewLogHandler(os.Stderr, slog.LevelInfo))`，宿主按结构化日志接收；stdout 只有握手行。
5. 宿主关闭 stdin 或调用 `Stop` 后 `Serve` 返回，进程退出。

验证：`xihan-conformance plugin --dir <产物目录>`（规范仓库的黑盒运行器，`go install github.com/xihanfun/framework-spec/conformance/cmd/xihan-conformance@latest`）。`examples/echo` 的测试就是这样对 `echo-go` 跑全部用例。运行器自带一份协议生成代码，与本 SDK 的 `plugin/v1` 不能链接进同一个二进制，所以在测试里以独立进程运行它，而不是导入它的库。

## 在 Go 应用里用原语

```go
ctx := invocation.Background(invocation.WithTimeout(2*time.Second), invocation.WithBaggage(map[string]string{primitives.BaggageTenant: "t1"}))
builder := pipeline.NewBuilder().UseAt(observe, primitives.Observe, pipeline.End, "app")
invoker, _ := operation.NewInvoker(registry, builder)
response := invoker.Invoke(ctx, operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":2}`)})
```

`Result[T]`、`Error`、`Maybe[T]` 的 JSON 编码与 .NET 参考实现一致（`{"ok":true,"value":…}` / `{"ok":false,"error":{…}}`），生命周期迁移表、管道阶段顺序、错误码与 Baggage 键文法都由 `spec/conformance/vectors` 验证。

`Error` 的线上编码（RFC-0001）：成员按 `code`、`message`、`messageKey`、`details`、`inner` 的顺序写出；`message` 总是写出，`messageKey` 只在有键时写（`Literal` 没有键），`details` 为空、`inner` 没有时不写。读取时 `code` 必填且合法，`messageKey` 有就必须合法，`details` 必须是 JSON 对象、`inner` 必须是错误对象（`null` 与数组都拒绝），成员名区分大小写，未知成员忽略。

## 破坏性改动与迁移

本节列出与规范、.NET 参考实现对齐时改掉的旧行为，以及调用方怎么改。

- `Error` 读取更严：`details` 为 `null` 或数组、`inner` 为 `null`、`messageKey` 不合文法（含空串）、`message` 不是字符串时 `json.Unmarshal` 返回 `PRIMITIVES_VALIDATION_001`，旧版会静默忽略。发送端按上面的编码写即可；`messageKey` 为空时不写这个成员。

## 开发

```bash
git submodule update --init          # spec/
buf generate                         # 改协议后重新生成 plugin/v1
gofmt -l . && go vet ./... && go test ./...
```

国内拉依赖：`GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.google.cn`。

## 状态

`v0.1`：原语、清单、上下文、管道、操作与插件运行时可用；流式操作（`InvokeStream`）尚未提供；Wasm 载体不在本 SDK 范围。版本随 `go/vN` 以外的普通标签发布（`v0.1.0`）。

## 许可

[MIT](LICENSE)
