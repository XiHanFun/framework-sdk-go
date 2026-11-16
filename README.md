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

`Result` 的线上编码（RFC-0001）：有值的成功 `{"ok":true,"value":…}`，`value` 必填，只有值类型是 `Maybe` 时才可以是 `null`；没有值的成功（`Result[Unit]`、`Success()`）`{"ok":true}`，不写 `value`；失败 `{"ok":false,"error":{…}}`，`error` 必填、不带 `value`。读取时 `ok` 缺失或不是布尔值、成功带 `error`、没有值的成功带 `value` 都拒绝，任何一层的重复成员名（如两个 `ok`，或 `value`、`details`、`inner` 里面的）也拒绝，成员名区分大小写，未知成员忽略。

`Error` 的线上编码（RFC-0001）：成员按 `code`、`message`、`messageKey`、`details`、`inner` 的顺序写出；`message` 总是写出，`messageKey` 只在有键时写（`Literal` 没有键），`details` 为空、`inner` 没有时不写。读取时 `code` 必填且合法，`message` 必填、是非空字符串，`messageKey` 有就必须合法，`details` 必须是 JSON 对象、`inner` 必须是错误对象（`null` 与数组都拒绝），成员名区分大小写，未知成员忽略。

读取的 JSON 文本（`Result` 与 `Error` 的线上编码、操作载荷、清单）先过同一遍检查（RFC-0001「JSON 文本」，ADR-0236）：语法按 RFC 8259（没有注释、尾逗号、单引号、`NaN` / `Infinity`、前导零、正号与多个根值，空文本与只有空白也不是 JSON），文本是 UTF-8，开头的 BOM 是语法错误；嵌套不超过 64 层（最外层是第 1 层）；字符串与成员名必须是合法的 Unicode，非法 UTF-8、过长编码、编码成 UTF-8 的代理项、原始控制字符、非法转义与孤立的代理项转义都拒绝；任何一层都不能有重复的成员名，名字按转义还原后比较（`"a"` 与 `"\u0061"` 重复），大小写不同不算重复。语法错误优先，位置只写 `$`；否则报第一处重复（第二次出现的成员）的路径。`encoding/json` 不查重复成员、把孤立的代理项换成 U+FFFD、嵌套上限是 10000，这一步由 SDK 内部的检查（`internal/jsontext`）先做，与一致性模块的 `rules.CheckJSONText` 回放同一份 `json-text.json`。

线上的数字按值模型往返（RFC-0001「Result」与 RFC-0010「模板」的值模型，ADR-0236）：`Result` 的值与 `Error` 的 `details`（插件协议 `Error.details` 的 JSON 文本也一样）读进 `any` 时，64 位有符号范围内整数写法的数字是 `int64`，原样往返（`9007199254740993` 不会变成 `9007199254740992`），其余是 `float64`，`1e400` 为无穷；写出时整数值的浮点带小数点（`2.0`、`-0.0`，范围外如 `1E+17`），对方读回的仍是浮点，`float32` 先换成最接近的 `float64`（`float32(0.1)` 写 `0.10000000149011612`），超出 `int64` 的无符号整数按浮点写，`NaN` 与无穷写成字符串 `"NaN"`、`"Infinity"`、`"-Infinity"`。字符串只转义必须转义的字符（`"`、`\`、控制字符），`<`、`>`、`&` 与非 ASCII 原样。类型化的值（`Result[Order]`、`Maybe[T]`、`ID[Tag, V]`）按与操作载荷相同的规则解码，见下文「操作载荷」。

## 破坏性改动与迁移

本节列出与规范、.NET 参考实现对齐时改掉的旧行为，以及调用方怎么改。

- `Success()` / `Result[Unit]` 编码为 `{"ok":true}`，旧版是 `{"ok":true,"value":{}}`；读取 `Result[Unit]` 时带 `value`（含 `{}`）即拒绝，不保留兼容路径。迁移：与旧版 Go 程序互通的一方同时升级；自己拼 JSON 的地方去掉 `"value":{}`。有值的成功缺 `value`、`value` 为 `null` 而值类型不是 `Maybe` 时旧版读成零值，现在拒绝；可能没有的值改用 `Result[Maybe[T]]`。写出时值编码成 `null`（如 `nil` 指针、`nil` 切片）而值类型不是 `Maybe` 也报错，空集合用非 `nil` 的空切片或空映射。
- 内核错误的码、键与参数与 .NET 一致（en-US 默认模板逐字照 .NET 的语言文件，由 `primitives.DefaultTemplate` 取得，`Error.Message` 在进程默认解析器解析不出时用它，再退回 `Describe`）：
  - 载荷不合法的键由 `kernel.operation.payload_invalid` 改为 `kernel.operation.invalid_payload`，参数由 `{reason}` 改为 `{id, reason}`，`reason` 只写出错位置的 JSON 路径（`$.quantity`、`$.lines[1].quantity`、`$['unit price']`，载荷是 `null`、不是对象或根上就不合时写 `$`），不再带 `encoding/json` 的报错文本与 Go 类型名（RFC-0011 规则 2）。成员名含 `. 空格 ' / " [ ] ( ) `、制表、换行、回车、换页、退格、U+0085、U+2028、U+2029 时写 `['名字']`、名字原样不转义，空名字写 `$.`；落在整数、浮点或布尔上的 `null`（`encoding/json` 悄悄当成零值）也算类型不符（ADR-0235）。新增 `operation.InvalidPayload(id, reason)`。宿主按键再翻译时旧键找不到文案。
  - 类型化调用类型不符的参数由 `{id}` 改为 `{id, expected, actual}`，值是「请求类型 -> 响应类型」。
  - 中间件以成功短路、处理器没有产出响应时，JSON 调用与类型化调用都返回 `KERNEL_UNEXPECTED_001`（`kernel.operation.no_response`，参数 `{id}`），旧版 JSON 调用返回 OK 加空载荷。迁移：以成功短路的中间件要么调用 `next`，要么返回失败。
  - `operation.Add` 的重复登记与登记表已满改为返回 `*primitives.Error`（`KERNEL_OPERATION_001` + `kernel.operation.duplicate`，参数 `{id, owner}`；`KERNEL_OPERATION_002` + `kernel.operation.registry_full`，参数 `{capacity}`），旧版是 `*primitives.FrameworkError`。迁移：`errors.As(err, &framework)` 改为 `errors.As(err, &primitivesError)`；ID 不合法仍是 `FrameworkError`。
  - 未初始化的 `Result` 的错误改为 `PRIMITIVES_VALIDATION_002` + `primitives.result.uninitialized`，旧版是没有键的文案。
  - 内核错误的 `message` 由 `Describe` 的形式（`kernel.operation.unknown (id=x)`）变为 en-US 文案（`Operation 'x' is not registered.`）；按文本匹配的地方改为按错误码匹配。
- `manifest.Read` 按 Schema 严格读取：成员名区分大小写（`encoding/json` 默认不区分，旧版 `"ID"` 也能读进 `id`）；任何位置的 `null` 都拒绝（旧版当作缺省）；`primitives` 与 `provides[].version` 只能是数字字符串，只有 `requires[].version` 可以带 `^`（旧版三处都接受 `^`）；Schema 的必填成员不能缺（旧版缺 `stability` 读成 `stable`、缺 `stage` 读成 `ingress`）；JSON 之后的多余内容拒绝；同一层的重复成员名拒绝（旧版取最后一个）；`activation` 写成空数组拒绝（旧版当作缺省，缺省才是 `onStartup`）；`contributes.operations` 按操作 ID 文法（连字符只在字母数字之间）。迁移：照 `spec/manifest/manifest.schema.json` 写清单，`"primitives": "^1"` 改 `"1"`。
- 管道的顺序约束按 RFC-0004 规则 7：`Before` / `After` 的两端可以写命名实例的实例名，也可以写只登记了一个实例的类型名；名字命中多个登记（同类型的多个实例按类型名引用、实例名与别的类型名相同）是 `KERNEL_PIPELINE_003`。报错的时机照 RFC-0004 规则 8（ADR-0235）：重复登记、未知阶段（`KERNEL_PIPELINE_002`）、约束主体（第一个参数）未登记或按名字有歧义在登记时立即报，是编程错误，`UseAt` / `UseNamedInstance` / `Before` / `After` 当场 panic `FrameworkError`（与 `http.ServeMux` 登记冲突时一样）；约束目标未登记、有歧义、跨阶段与成环在排序时报，由 `Describe` / `Dump` / `Build` 返回。旧版把这些都推迟到 `Describe` 才返回，先写约束、后登记主体也能通过。迁移：先登记中间件再声明它的约束（目标仍可以晚于约束登记）；按动态数据登记中间件的代码在登记前自己排重，或用 `recover` 接住 `*primitives.FrameworkError`。
- 已登记 Baggage 键的值按 conventions/baggage-keys.md 核对（ADR-0204、ADR-0233）：非空、按 UTF-16 码元计不超过 256（基本多文种平面以外的字符算两个）、不含 U+0000–U+001F 与 U+007F–U+009F。外部输入的入口（选项 `invocation.WithBaggage`，插件运行时从元数据建上下文也走它）遇到不合的值丢弃这个成员、原有的同名值保留；程序内逐项派生 `Context.WithBaggage(key, value)` 给已登记的键一个不合的值是编程错误，与键不合文法一样 panic `FrameworkError`（`PRIMITIVES_VALIDATION_001`，ADR-0235），不静默丢弃也不保留原值。旧版两处都原样放进上下文。迁移：派生前用 `primitives.IsBaggageEntryAcceptable` 核对来自外部的值，或改用选项 `WithBaggage` 让不合的值被丢弃；`xihan.idempotency-key` 例外，留给 Idempotency 阶段拒绝；未登记的键不在这里核对。新增 `primitives.IsBaggageValueAcceptable`、`IsBaggageEntryAcceptable` 与 `MaxBaggageValueLength`。插件运行时从元数据建上下文时随之丢弃。
- 追踪标志按 RFC-0003 规则 7：没有上游时 `Context.TraceFlags()` 为空（这次调用是链路的根），旧版一律当作已采样；写出 `traceparent` 时标志只写采样位（`00` 或 `01`），上游的 `00` 原样传下去，其余位不进上下文（ADR-0235；旧版把 `03` 这样的标志原样写出），没有上游时 SDK 没有采样器、写 `01`；`traceparent` 只认版本 `00` 与小写十六进制，`ff` 等其他版本当作没给（旧版接受任意两位版本）；`WithTrace` 只给 ID 不给标志按已采样处理。是否采样只看标志的最低位（`invocation.IsSampled`），旧版只认 `01`，`03` 也算不采样。
- `operation.Invoker` 把中间件或处理器的 panic 翻译为 `KERNEL_UNEXPECTED_001`（`kernel.invocation.unhandled_exception`，没有参数），与 .NET Observe 阶段翻译未处理的异常一致（RFC-0011 规则 5，规则 10 要求语言 SDK 实现）；panic 的值与堆栈只写进 `slog.Default()` 的日志，值是 `FrameworkError` 时作为内部错误带上。旧版不恢复 panic，插件进程随之崩溃。要让进程退出的处理器改用 `os.Exit`。
- 超时与取消的归因只看截止令牌（RFC-0004、ADR-0092）：`invocation.Context.Cancellation()` 在截止时间已到时报 `KERNEL_TIMEOUT_001`，即使调用方先取消了；截止令牌没有触发时报 `KERNEL_CANCELLED_001`。旧版按先触发的那个报，调用方先取消、随后到了截止时间时仍是取消。新增 `Context.DeadlineExceeded()`。`operation.Invoker` 承担 .NET `DeadlineMiddleware` 的归因：调用开始时截止时间已过即 `KERNEL_TIMEOUT_001`（`kernel.invocation.deadline_before_start`），不进管道；下游写下 `KERNEL_CANCELLED_001` 而截止令牌已触发时改写为 `KERNEL_TIMEOUT_001`。按错误码决定重试的调用方要按超时处理这类调用。
- `operation.Current`、`RequestOf`、`ResponsePayloadOf` 只在调用期间、在经管道传下来的上下文上可见：每次调用的状态（描述、请求、终点、响应载荷）放在为这次调用派生的上下文里，不再写进对象袋，调用结束后调用方的上下文与对象袋保持原样。旧版把它们留在对象袋里，类型化调用的终点不清，同一上下文随后的 JSON 调用会执行上一次的类型化终点（缺陷）。调用结束后还在调用方上下文上读这三项的代码要改为在中间件里读。
- `primitives.Unexpected` 的参数改为 `(code, messageKey, details, inner)`，与其他工厂和 `NewError` 一致（RFC-0001、ADR-0137）；旧版是 `(code, messageKey, inner, details)`。两个参数类型不同，旧调用编译即报错，交换后两个实参即可。
- 模板的格式说明符改为可移植子集（RFC-0010），不再按 Go 的 `fmt` 动词解释：`D[n]`（整数，最少 n 位补零）、`X[n]` / `x[n]`（非负整数的十六进制）、`F[n]`（定点小数，缺省 2 位）、`N[n]`（千分位 `,`、小数点 `.`，缺省 2 位）；F 与 N 按值的精确十进制展开舍入，正好一半时取偶（四舍六入五成双，与 .NET 一致：0.125 写 `F2` 得 `0.12`，2.5 写 `F0` 得 `2`）。D、F、N 不区分大小写（`d3` 等同 `D3`，旧版把小写当成不支持），精度 0–99 都支持。其他说明符（日期、自定义数字模式）是 .NET 格式串，SDK 按无格式输出。迁移：`{count:%03d}` 改 `{count:D3}`，`{n:%x}` 改 `{n:x}`，`{n:%.2f}` 改 `{n:F2}`；插件目录在握手时交给 .NET 宿主按 .NET 规则格式化，旧的 `%` 写法到了宿主那里会被当作自定义数字格式，必须改。
- 模板支持 `{{` / `}}` 转义；占位符名按 `^([A-Za-z][A-Za-z0-9_]*|[0-9]+)$`，下划线开头的 `{_x}` 不再是占位符而是字面量；没有闭合的 `{` 及其后的文本原样保留。
- `primitives.Catalog.Resolve` 改为严格解析：找不到键或任一占位符缺值即返回假，`Error.Message` 随之退回 `Describe`；旧版缺值时仍返回半截译文。需要宽松格式化时直接用 `FormatMessage`，需要严格格式化时用新增的 `TryFormatMessage`。区域名比较不再区分大小写。
- 消息参数的文本形式改由 `primitives.FormatValue` 统一（模板与 `Describe` 共用，RFC-0010）：`nil` 与 `nil` 指针写空串，旧版写 `<nil>`；浮点在 1e-4 ≤ |x| < 1e17 或 x = 0 时写最短往返的十进制、不用指数，`2.0` 写 `2`、1e16 写 `10000000000000000`；范围之外写 `1E+17`、`9.9E-05`、`5E-324` 这样的指数形式，负零写 `-0`，非数与无穷写 `NaN`、`Infinity`、`-Infinity`（ADR-0235）。F 与 N 写出全部整数位，负数舍入成零时保留负号（`-0.00`）。依赖旧文本（如在日志里匹配 `<nil>`）的地方要改。
- `Result` 与 `Error` 里同一层的重复成员名不合法（ADR-0235），旧版由 `encoding/json` 取最后一个。`Error` 读取更严：`details` 为 `null` 或数组、`inner` 为 `null`、`messageKey` 不合文法（含空串）、`message` 缺失、为空串或不是字符串时 `json.Unmarshal` 返回 `PRIMITIVES_VALIDATION_001`，旧版会静默忽略。发送端按上面的编码写即可；`messageKey` 为空时不写这个成员。
- JSON 文本的共同读取检查（RFC-0001「JSON 文本」，ADR-0236）：`Result` 与 `Error` 读取时开头的 BOM、嵌套超过 64 层、孤立的代理项转义（`"\ud800"`）与非法 UTF-8 都拒绝，旧版由 `encoding/json` 接受（孤立的代理项换成 U+FFFD）。操作载荷先过同一遍检查：语法错误（含 BOM、嵌套超过 64 层、孤立的代理项）的 `reason` 一律 `$`；任何一层的重复成员是 `KERNEL_VALIDATION_001`，`reason` 是第一处重复的路径，先于其他问题（旧版 `encoding/json` 取最后一个）；只有空白的载荷不再等价于 `{}`（空载荷仍等价于 `{}`），按语法错误处理。`manifest.Read` 同样拒绝这些写法。迁移：发送合法的 UTF-8 JSON，不在文本前加 BOM，生成 JSON 的代码去掉重复成员。
- 线上的数字按值模型往返（ADR-0236）：`Result[any]`、`Result[map[string]any]` 的值与 `Error.Details` 里的整数读成 `int64`，旧版由 `encoding/json` 读成 `float64`（超过 2^53 的整数因此失真）；整数值的浮点写成 `2.0`、`-0.0`（旧版写 `2`、`-0`，对方读成整数）；`NaN` 与无穷写成字符串（旧版 `json.Marshal` 报错，整个 `Result` 写不出）。插件协议的 `Error.details` 照同一模型读写，读回的数组与对象是 `[]any` 与 `map[string]any`，不是合法 JSON 对象的细节丢弃。`Result`、`Maybe`、`ID` 的类型化值改用 SDK 的解码：成员名区分大小写（旧版 `"Count"` 也能读进 `count`）、整数不接受 `1.0`、整数与布尔不接受 `null`、数字不从字符串读。迁移：按 `float64` 断言细节里整数的代码改为同时接受 `int64`（或用 `primitives.FormatValue` 取文本）；发送端照字段的 JSON 名字写成员。

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
