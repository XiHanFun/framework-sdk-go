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

1. 清单 `xihan.manifest.json`：`kind: plugin`、`isolation: out-of-process`、`contributes.operations` 列出全部操作、`entry` 是可执行文件名；不写 `contributes.pipeline`（只有 Wasm 插件能贡献中间件，`plugin.Serve` 读到非空即拒绝，与宿主发现插件时一致；`manifest.Read` 照 .NET 的 `ManifestReader` 不查这一条）；构建产物目录里放同一份文件，宿主读它、运行时握手时原样发送。
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

### 操作载荷

`operation.Invoker.Invoke` 把载荷解码成请求类型的规则照 RFC-0011 规则 2（ADR-0236，与 .NET 的 System.Text.Json 缺省读取选项一致，不随 `encoding/json` 的习惯）。失败都是 `KERNEL_VALIDATION_001` + `kernel.operation.invalid_payload`，`reason` 是第一处问题的 JSON 路径：

- 空载荷等价于 `{}`；先过上面的共同检查，语法错误写 `$`，重复成员写第一处重复的路径；载荷是 `null` 写 `$`。
- 成员名区分大小写（`encoding/json` 不区分），未知成员忽略；数字不从字符串读，字符串也不从数字读（字段带 `json:",string"` 时除外）。
- 整数字段不接受小数点与指数（`1.0`、`1e2` 都不是整数），超出字段类型的范围即不符（`int32` 字段收到 `2147483648`）；浮点字段接受整数写法，`1e400` 上溢为无穷，`1e-400` 为 0，`float32` 按 32 位的范围读。
- `null`：字符串、切片、数组、映射、结构体、指针与 `any` 接受，等于没有值（字段保持零值）；整数、浮点与布尔（含它们的切片项与映射值）不接受。要接受 `null` 的数值或布尔写成指针（`*int64`、`*bool`）。
- 必填：字段带 `xihan:"required"` 标签即必填（对应 C# 的 `required`），缺少时在所在对象结束时报该对象的路径（根上为 `$`，嵌套如 `$.inner`）；只要求成员出现，值为 `null` 也算出现。没有这个标签的成员缺少时取零值。
- 按文本顺序报第一处问题：写在前面的类型不符先报，嵌套对象里缺少的成员先于外层后面的类型不符。
- 实现 `json.Unmarshaler` 的类型（`time.Time`、`json.RawMessage` 等）拿到原文自己解码，失败报它的路径；实现 `encoding.TextUnmarshaler` 的类型只接字符串；`any` 按值模型读；`[]byte` 读 base64 字符串。字段名、嵌入结构体的提升与 `json` 标签照 `encoding/json`。

```go
type CreateOrder struct {
	Customer string        `json:"customer" xihan:"required"` // 必填，null 也算出现
	Note     string        `json:"note"`                       // 可选，null 等于没有
	Quantity int32         `json:"quantity"`                   // 可选，不接受 null 与 1.0
	Discount *float64      `json:"discount"`                   // 可选，接受 null
	Lines    []OrderLine   `json:"lines"`
	Extra    any           `json:"extra"`                      // 任意 JSON，按值模型
}
```

### 清单

`manifest.Read` 的检查顺序与错误照 RFC-0002 解析规则 2（ADR-0236，与 .NET 的 `ManifestReader` 逐字一致），每一步只报第一处问题，失败都是 `*primitives.Error`，错误码 `KERNEL_CAPABILITY_006`（`manifest.ErrorCodeInvalidManifest`）：

1. 共同的 JSON 读取检查：不是 JSON 为 `kernel.manifest.invalid_json`，`reason` 是 `$`；有重复成员时同一个键，`reason` 是第一处重复的路径。`Read` 不跳过 BOM；`ReadFile` 按 UTF-8 读、跳过开头的 UTF-8 BOM，文件不存在为 `kernel.manifest.file_missing`（参数 `path`）。
2. 根是 `null`：`kernel.manifest.empty`，没有参数。
3. 按 Schema 逐项核对：`kernel.manifest.invalid`，`reason` 是第一处问题的路径，如 `$.provides[0].version`。成员按文本顺序，各自依次查 Schema 之外的成员（区分大小写）、`null`、类型、取值，对象与数组进到里面再查；一个对象查完后按 `required` 的顺序查缺少的成员。接口版本是不带前导零的正整数，不超过 2147483647，只有 `requires[].version` 可以带一个 `^`；中间件名非空，阶段与枚举只认小写名，`contributes.health` 符合 `^[a-z][a-z0-9.-]*$`，`contributes.permissions` 重复时报第二次出现的项，`activation` 至少一项。
4. 跨字段约束：插件没有 `isolation`、非插件写了 `isolation` 或 `permissions`、`provides` 重复、`requires` 重复、`bindings` 的名字重复，按这个顺序，键是 `primitives.manifest.*`，参数只有 `id`。

`Manifest.Validate` 先按 Schema 核对清单写出的形态，再核对跨字段约束，码是 `PRIMITIVES_VALIDATION_001`（与 .NET 的 `Manifest.Validate` 一致）。`manifest.Write` 的排版写死（RFC-0002「写出」）：两个空格缩进、LF、末尾不换行、`": "`、空数组写 `[]`、成员顺序固定、列表总写、`role` 缺省写 `provider`、`activation` 缺省写 `["onStartup"]`、`$schema` 固定，字符串只转义必须转义的字符；与 .NET 一样不做校验。可选的字符串 `Package`、`Entry` 与 `ConfigContribution.Schema` 是 `*string`：`nil` 是没写、写出时不写，指向空串是写了空串、写出 `"package": ""`；用 `manifest.Text("…")` 赋值。

### 消息模板

模板参数先归到 RFC-0010「模板」的值模型（ADR-0236）——空、布尔、字符串、64 位整数、双精度浮点、数组与对象——再按说明符与区域写出。Go 的类型这样归：

| Go 的值 | 值模型 | 说明 |
| --- | --- | --- |
| `nil`、`nil` 指针、`nil` 切片与映射 | 空 | 顶层写空串，嵌套写 `null` |
| `bool` 与底层是 `bool` 的类型 | 布尔 | `true` / `false` |
| `string` 与底层是 `string` 的类型 | 字符串 | 原样 |
| `int`、`int8`…`int64`，`uint`…`uint64` 在 int64 范围内 | 整数 | `rune` 是 `int32`，按整数写；要写一个字符用 `string(r)` |
| 超出 int64 的 `uint64`、`*big.Int` | 浮点 | `D` 与 `X` 不再适用 |
| `float64`、`float32`、`*big.Float`、`*big.Rat` | 浮点 | `float32` 换成最接近的 `float64`：`float32(0.1)` 写 `0.10000000149011612` |
| `json.Number`、`json.RawMessage` | 按 JSON 读 | 整数写法且在 64 位范围内是整数，其余数字是浮点 |
| 切片、数组（含 `[]byte`） | 数组 | |
| 映射 | 对象 | 键写成它的文本 |
| 实现 `error` / `fmt.Stringer` 的值 | 字符串 | 取 `Error()` / `String()`：`time.Time`、`time.Duration`、带 `String` 的枚举（`primitives.Stability`）都是 Go 自己的文本 |
| 其余（结构体、复数等） | 字符串 | `fmt.Sprint` 的文本 |

Go 自己的文本（日期、`Duration`、结构体）不跨语言：插件的目录要在宿主上格式化时，先把这些值转成字符串再放进参数。数组与对象写成紧凑的 JSON，对象成员按名字的 UTF-16 码元序排序、嵌套的 `null` 写 `null`、数字与顶层写法相同，超过 64 层的容器写 `null`。说明符只认 `^[DdFfNnXx][0-9]{0,2}$`，`E2`、`G`、`P`、`0.00`、`yyyy`、空说明符、带空格、`D100` 都按无格式写；`D` 与 `X` 只用于整数，`X` 只用于非负整数，`F` 与 `N` 用于整数与浮点，用在别的值上都按无格式写。

格式化可以带区域：`primitives.FormatMessageIn(locale, template, arguments)`、`TryFormatMessageIn`、`FormatArgumentIn(locale, value, format)`，`Catalog.Resolve` 按命中模板的区域取符号（回退到 en-US 的模板按 en-US 写）。区域只改变小数点与分组符，取自规范的固定表：de-DE、es-ES、pt-BR 是 `,` 与 `.`，fr-FR 是 `,` 与 U+202F，ru-RU 是 `,` 与 U+00A0，en-US、zh-CN、zh-TW、ja-JP、ko-KR、表外的区域与不给区域都是 `.` 与 `,`，区域名不区分大小写。小数点用在无格式的浮点（含指数写法的尾数，de-DE 的 `1.5e20` 写 `1,5E+20`）与 `F`、`N` 上，分组符只用在 `N` 上；整数无格式时不分组，`D`、`X` 与数组、对象里的数字不受区域影响，负号、`NaN`、`Infinity` 也不变。不带区域的 `FormatMessage`、`TryFormatMessage`、`FormatArgument`、`FormatValue` 与 `Describe` 按不变文化。

### 调用器的内置步骤

`operation.NewInvoker` 在传入的管道的副本里加上三个内置步骤（RFC-0011 规则 10，ADR-0236），名字与 .NET 的内置中间件相同，都按归属 `xihan.kernel.core`（`pipeline.KernelOwner`）放在所在阶段的 `Beginning`，与同位置的其他中间件按 RFC-0004 的规则排序（归属的序数序，`app` 排在它前面）：

- Observe 阶段 `ObserveMiddleware`：把排在它之后的中间件与处理器的 panic 翻译为 `KERNEL_UNEXPECTED_001` + `kernel.invocation.unhandled_exception`；排在它之前的（Ingress 阶段，或 Observe 阶段 `Beginning` 上归属排在 `xihan.kernel.core` 之前的）panic 原样交给调用方。
- Resilience 阶段 `DeadlineMiddleware`：进入时截止时间已到即 `KERNEL_TIMEOUT_001` + `kernel.invocation.deadline_before_start`，后面的中间件与处理器不执行；进入时不核对取消；途中的超时与取消按截止令牌归因。
- Validate 阶段 `ValidationMiddleware`：原始调用（`Invoker.Invoke`）在这里把载荷解码成请求，失败即 `KERNEL_VALIDATION_001`；类型化调用（`operation.Invoke`）跳过。

进入管道前只核对未登记（`KERNEL_NOTFOUND_001`）与类型化调用的类型不符（`KERNEL_VALIDATION_002`），按这个顺序。`Invoker.Pipeline()` 返回实际执行的管道（含内置步骤）。

## 破坏性改动与迁移

本节列出与规范、.NET 参考实现对齐时改掉的旧行为，以及调用方怎么改。

- `Success()` / `Result[Unit]` 编码为 `{"ok":true}`，旧版是 `{"ok":true,"value":{}}`；读取 `Result[Unit]` 时带 `value`（含 `{}`）即拒绝，不保留兼容路径。迁移：与旧版 Go 程序互通的一方同时升级；自己拼 JSON 的地方去掉 `"value":{}`。有值的成功缺 `value`、`value` 为 `null` 而值类型不是 `Maybe` 时旧版读成零值，现在拒绝；可能没有的值改用 `Result[Maybe[T]]`。写出时值编码成 `null`（如 `nil` 指针、`nil` 切片）而值类型不是 `Maybe` 也报错，空集合用非 `nil` 的空切片或空映射。
- 内核错误的码、键与参数与 .NET 一致（en-US 默认模板逐字照 .NET 的语言文件，由 `primitives.DefaultTemplate` 取得，`Error.Message` 在进程默认解析器解析不出时用它，再退回 `Describe`）：
  - 载荷不合法的键由 `kernel.operation.payload_invalid` 改为 `kernel.operation.invalid_payload`，参数由 `{reason}` 改为 `{id, reason}`，`reason` 只写出错位置的 JSON 路径（`$.quantity`、`$.lines[1].quantity`、`$['unit price']`，载荷是 `null`、不是对象或根上就不合时写 `$`），不再带 `encoding/json` 的报错文本与 Go 类型名（RFC-0011 规则 2）。成员名含 `. 空格 ' / " [ ] ( ) `、制表、换行、回车、换页、退格、U+0085、U+2028、U+2029 时写 `['名字']`、名字原样不转义，空名字写 `$.`；落在整数、浮点或布尔上的 `null`（`encoding/json` 悄悄当成零值）也算类型不符（ADR-0235）。新增 `operation.InvalidPayload(id, reason)`。宿主按键再翻译时旧键找不到文案。
  - 类型化调用类型不符（`KERNEL_VALIDATION_002` + `kernel.operation.type_mismatch`）只带 `{id}`（ADR-0236），en-US 模板为 `Operation '{id}' was invoked with request or response types that do not match its registration.`；上一版带的 `expected`、`actual` 是 Go 的类型名，各语言写不出同样的参数，已去掉。迁移：读 `expected` / `actual` 的代码改为只用 `id`，要看类型时在调用方自己记日志。
  - 中间件以成功短路、处理器没有产出响应时，JSON 调用与类型化调用都返回 `KERNEL_UNEXPECTED_001`（`kernel.operation.no_response`，参数 `{id}`），旧版 JSON 调用返回 OK 加空载荷。迁移：以成功短路的中间件要么调用 `next`，要么返回失败。
  - `operation.Add` 的重复登记与登记表已满改为返回 `*primitives.Error`（`KERNEL_OPERATION_001` + `kernel.operation.duplicate`，参数 `{id, owner}`；`KERNEL_OPERATION_002` + `kernel.operation.registry_full`，参数 `{capacity}`），旧版是 `*primitives.FrameworkError`。迁移：`errors.As(err, &framework)` 改为 `errors.As(err, &primitivesError)`；ID 不合法仍是 `FrameworkError`。
  - 未初始化的 `Result` 的错误改为 `PRIMITIVES_VALIDATION_002` + `primitives.result.uninitialized`，旧版是没有键的文案。
  - 内核错误的 `message` 由 `Describe` 的形式（`kernel.operation.unknown (id=x)`）变为 en-US 文案（`Operation 'x' is not registered.`）；按文本匹配的地方改为按错误码匹配。
- `manifest.Read` 按 Schema 严格读取：成员名区分大小写（`encoding/json` 默认不区分，旧版 `"ID"` 也能读进 `id`）；任何位置的 `null` 都拒绝（旧版当作缺省）；`primitives` 与 `provides[].version` 只能是数字字符串，只有 `requires[].version` 可以带 `^`（旧版三处都接受 `^`）；Schema 的必填成员不能缺（旧版缺 `stability` 读成 `stable`、缺 `stage` 读成 `ingress`）；JSON 之后的多余内容拒绝；同一层的重复成员名拒绝（旧版取最后一个）；`activation` 写成空数组拒绝（旧版当作缺省，缺省才是 `onStartup`）；`contributes.operations` 按操作 ID 文法（连字符只在字母数字之间）。迁移：照 `spec/manifest/manifest.schema.json` 写清单，`"primitives": "^1"` 改 `"1"`。
- 管道的顺序约束按 RFC-0004 规则 7：`Before` / `After` 的两端可以写命名实例的实例名，也可以写只登记了一个实例的类型名；名字命中多个登记（同类型的多个实例按类型名引用、实例名与别的类型名相同）是 `KERNEL_PIPELINE_003`。报错的时机照 RFC-0004 规则 8（ADR-0235）：重复登记、未知阶段（`KERNEL_PIPELINE_002`）、约束主体（第一个参数）未登记或按名字有歧义在登记时立即报，是编程错误，`UseAt` / `UseNamedInstance` / `Before` / `After` 当场 panic `FrameworkError`（与 `http.ServeMux` 登记冲突时一样）；约束目标未登记、有歧义、跨阶段与成环在排序时报，由 `Describe` / `Dump` / `Build` 返回。旧版把这些都推迟到 `Describe` 才返回，先写约束、后登记主体也能通过。迁移：先登记中间件再声明它的约束（目标仍可以晚于约束登记）；按动态数据登记中间件的代码在登记前自己排重，或用 `recover` 接住 `*primitives.FrameworkError`。
- 已登记 Baggage 键的值按 conventions/baggage-keys.md 核对（ADR-0204、ADR-0233）：非空、按 UTF-16 码元计不超过 256（基本多文种平面以外的字符算两个）、不含 U+0000–U+001F 与 U+007F–U+009F。外部输入的入口（选项 `invocation.WithBaggage`，插件运行时从元数据建上下文也走它）遇到不合的值丢弃这个成员、原有的同名值保留；程序内逐项派生 `Context.WithBaggage(key, value)` 给已登记的键一个不合的值是编程错误，与键不合文法一样 panic `FrameworkError`（`PRIMITIVES_VALIDATION_001`，ADR-0235），不静默丢弃也不保留原值。旧版两处都原样放进上下文。迁移：派生前用 `primitives.IsBaggageEntryAcceptable` 核对来自外部的值，或改用选项 `WithBaggage` 让不合的值被丢弃；`xihan.idempotency-key` 例外，留给 Idempotency 阶段拒绝；未登记的键不在这里核对。新增 `primitives.IsBaggageValueAcceptable`、`IsBaggageEntryAcceptable` 与 `MaxBaggageValueLength`。插件运行时从元数据建上下文时随之丢弃。
- 追踪标志按 RFC-0003 规则 7：没有上游时 `Context.TraceFlags()` 为空（这次调用是链路的根），旧版一律当作已采样；写出 `traceparent` 时标志只写采样位（`00` 或 `01`），上游的 `00` 原样传下去，其余位不进上下文（ADR-0235；旧版把 `03` 这样的标志原样写出），没有上游时 SDK 没有采样器、写 `01`；`traceparent` 只认版本 `00` 与小写十六进制，`ff` 等其他版本当作没给（旧版接受任意两位版本）；`WithTrace` 只给 ID 不给标志按已采样处理。是否采样只看标志的最低位（`invocation.IsSampled`），旧版只认 `01`，`03` 也算不采样。
- `operation.Invoker` 把中间件或处理器的 panic 翻译为 `KERNEL_UNEXPECTED_001`（`kernel.invocation.unhandled_exception`，没有参数），与 .NET Observe 阶段翻译未处理的异常一致（RFC-0011 规则 5，规则 10 要求语言 SDK 实现）；panic 的值与堆栈只写进 `slog.Default()` 的日志，值是 `FrameworkError` 时作为内部错误带上。旧版不恢复 panic，插件进程随之崩溃。要让进程退出的处理器改用 `os.Exit`。
- 超时与取消的归因只看截止令牌（RFC-0004、ADR-0092）：`invocation.Context.Cancellation()` 在截止时间已到时报 `KERNEL_TIMEOUT_001`，即使调用方先取消了；截止令牌没有触发时报 `KERNEL_CANCELLED_001`。旧版按先触发的那个报，调用方先取消、随后到了截止时间时仍是取消。新增 `Context.DeadlineExceeded()`。`operation.Invoker` 的内置步骤 `DeadlineMiddleware` 承担 .NET `DeadlineMiddleware` 的归因：进入 Resilience 阶段时截止时间已到即 `KERNEL_TIMEOUT_001`（`kernel.invocation.deadline_before_start`）；下游写下 `KERNEL_CANCELLED_001` 而截止令牌已触发时改写为 `KERNEL_TIMEOUT_001`。按错误码决定重试的调用方要按超时处理这类调用。
- `operation.Current`、`RequestOf`、`ResponsePayloadOf` 只在调用期间、在经管道传下来的上下文上可见：每次调用的状态（描述、请求、终点、响应载荷）放在为这次调用派生的上下文里，不再写进对象袋，调用结束后调用方的上下文与对象袋保持原样。旧版把它们留在对象袋里，类型化调用的终点不清，同一上下文随后的 JSON 调用会执行上一次的类型化终点（缺陷）。调用结束后还在调用方上下文上读这三项的代码要改为在中间件里读。
- `primitives.Unexpected` 的参数改为 `(code, messageKey, details, inner)`，与其他工厂和 `NewError` 一致（RFC-0001、ADR-0137）；旧版是 `(code, messageKey, inner, details)`。两个参数类型不同，旧调用编译即报错，交换后两个实参即可。
- 模板的格式说明符改为可移植子集（RFC-0010），不再按 Go 的 `fmt` 动词解释：`D[n]`（整数，最少 n 位补零）、`X[n]` / `x[n]`（非负整数的十六进制）、`F[n]`（定点小数，缺省 2 位）、`N[n]`（千分位 `,`、小数点 `.`，缺省 2 位）；F 与 N 按值的精确十进制展开舍入，正好一半时取偶（四舍六入五成双，与 .NET 一致：0.125 写 `F2` 得 `0.12`，2.5 写 `F0` 得 `2`）。D、F、N 不区分大小写（`d3` 等同 `D3`，旧版把小写当成不支持），精度是字母后的 0–2 位数字（ADR-0236 起，见下文）。其他说明符（日期、自定义数字模式）是 .NET 格式串，SDK 按无格式输出。迁移：`{count:%03d}` 改 `{count:D3}`，`{n:%x}` 改 `{n:x}`，`{n:%.2f}` 改 `{n:F2}`；插件目录在握手时交给 .NET 宿主按 .NET 规则格式化，旧的 `%` 写法到了宿主那里会被当作自定义数字格式，必须改。
- 模板支持 `{{` / `}}` 转义；占位符名按 `^([A-Za-z][A-Za-z0-9_]*|[0-9]+)$`，下划线开头的 `{_x}` 不再是占位符而是字面量；没有闭合的 `{` 及其后的文本原样保留。
- `primitives.Catalog.Resolve` 改为严格解析：找不到键或任一占位符缺值即返回假，`Error.Message` 随之退回 `Describe`；旧版缺值时仍返回半截译文。需要宽松格式化时直接用 `FormatMessage`，需要严格格式化时用新增的 `TryFormatMessage`。区域名比较不再区分大小写。
- 消息参数的文本形式改由 `primitives.FormatValue` 统一（模板与 `Describe` 共用，RFC-0010）：`nil` 与 `nil` 指针写空串，旧版写 `<nil>`；浮点在 1e-4 ≤ |x| < 1e17 或 x = 0 时写最短往返的十进制、不用指数，`2.0` 写 `2`、1e16 写 `10000000000000000`；范围之外写 `1E+17`、`9.9E-05`、`5E-324` 这样的指数形式，负零写 `-0`，非数与无穷写 `NaN`、`Infinity`、`-Infinity`（ADR-0235）。F 与 N 写出全部整数位，负数舍入成零时保留负号（`-0.00`）。依赖旧文本（如在日志里匹配 `<nil>`）的地方要改。
- `Result` 与 `Error` 里同一层的重复成员名不合法（ADR-0235），旧版由 `encoding/json` 取最后一个。`Error` 读取更严：`details` 为 `null` 或数组、`inner` 为 `null`、`messageKey` 不合文法（含空串）、`message` 缺失、为空串或不是字符串时 `json.Unmarshal` 返回 `PRIMITIVES_VALIDATION_001`，旧版会静默忽略。发送端按上面的编码写即可；`messageKey` 为空时不写这个成员。
- JSON 文本的共同读取检查（RFC-0001「JSON 文本」，ADR-0236）：`Result` 与 `Error` 读取时开头的 BOM、嵌套超过 64 层、孤立的代理项转义（`"\ud800"`）与非法 UTF-8 都拒绝，旧版由 `encoding/json` 接受（孤立的代理项换成 U+FFFD）。操作载荷先过同一遍检查：语法错误（含 BOM、嵌套超过 64 层、孤立的代理项）的 `reason` 一律 `$`；任何一层的重复成员是 `KERNEL_VALIDATION_001`，`reason` 是第一处重复的路径，先于其他问题（旧版 `encoding/json` 取最后一个）；只有空白的载荷不再等价于 `{}`（空载荷仍等价于 `{}`），按语法错误处理。`manifest.Read` 同样拒绝这些写法。迁移：发送合法的 UTF-8 JSON，不在文本前加 BOM，生成 JSON 的代码去掉重复成员。
- 线上的数字按值模型往返（ADR-0236）：`Result[any]`、`Result[map[string]any]` 的值与 `Error.Details` 里的整数读成 `int64`，旧版由 `encoding/json` 读成 `float64`（超过 2^53 的整数因此失真）；整数值的浮点写成 `2.0`、`-0.0`（旧版写 `2`、`-0`，对方读成整数）；`NaN` 与无穷写成字符串（旧版 `json.Marshal` 报错，整个 `Result` 写不出）。插件协议的 `Error.details` 照同一模型读写，读回的数组与对象是 `[]any` 与 `map[string]any`，不是合法 JSON 对象的细节丢弃。`Result`、`Maybe`、`ID` 的类型化值改用 SDK 的解码：成员名区分大小写（旧版 `"Count"` 也能读进 `count`）、整数不接受 `1.0`、整数与布尔不接受 `null`、数字不从字符串读。迁移：按 `float64` 断言细节里整数的代码改为同时接受 `int64`（或用 `primitives.FormatValue` 取文本）；发送端照字段的 JSON 名字写成员。
- 操作载荷按 RFC-0011 规则 2 解码（ADR-0236），不再交给 `encoding/json`：成员名区分大小写（旧版 `"Quantity"` 也能读进 `quantity`）；`int32` 等窄整数按自己的范围核对；浮点字段的 `1e400` 读成无穷（旧版报类型不符）；`null` 落在整数、浮点、布尔的切片项与映射值上也报类型不符；新增必填标签 `xihan:"required"`；几处问题同时存在时按文本顺序报第一处，缺少的必填成员在所在对象结束时报。迁移：调用方照字段的 JSON 名字写成员；需要接受 `null` 的数值改用指针；契约要求必填的成员加上 `xihan:"required"`。
- 清单的读取与写出与 .NET 逐字一致（RFC-0002，ADR-0236）：`manifest.Read`、`ReadFile` 的错误由 `*primitives.FrameworkError`（`PRIMITIVES_VALIDATION_001`，英文说明）改为 `*primitives.Error`，码 `KERNEL_CAPABILITY_006`，键是 `kernel.manifest.invalid_json` / `empty` / `invalid` / `file_missing` 或 `primitives.manifest.*`，参数只有路径（`reason`）、`path` 或 `id`；`Validate` 也返回 `*primitives.Error`。接口版本超过 2147483647 拒绝（旧版到 int 的上限，`ParseRequirement`、`ParseDeclaration` 同样）；中间件名为空、`contributes.health` 不合文法开始拒绝。`ReadFile` 跳过开头的 UTF-8 BOM，`Read` 不跳过。`Write` 改为规范的排版：列表总写（`[]`）、`role` 与 `activation` 写出缺省值、`requires[].optional` 与 `bindings[].required` 总写、字符串不再转义 `<`、`>`、`&` 与非 ASCII，`Write` 不再校验（与 .NET 一样），`Activation` 为空切片时写 `["onStartup"]`。只有 Wasm 插件能贡献中间件的规则从 `manifest.Read` 移到 `plugin.Serve`（.NET 宿主在发现插件时核对，`ManifestReader` 不核对）。迁移：按错误码匹配的地方改为 `errors.As(err, &primitivesError)` 并看 `Code` 与 `MessageKey`；按英文说明匹配的地方改为看路径；比较写出文本的测试更新期望值。
- 模板的值按值模型写出（RFC-0010「模板」，ADR-0236）：数组与对象写成紧凑的 JSON（旧版是 `fmt.Sprint` 的 `[1 a]`、`map[a:1]`）；超出 int64 的 `uint64` 是浮点（旧版写全部数字，`D` 照样补零）；`float32` 按 `float64` 的值写（`float32(0.1)` 由 `0.1` 变为 `0.10000000149011612`）；`json.Number` 的小数写法是浮点，`F` 按二进制值舍入（`2.675` 写 `F2` 由 `2.68` 变为 `2.67`），`7.50` 写 `7.5`。说明符只认 `^[DdFfNnXx][0-9]{0,2}$`：精度最多两位（旧版到 999），`D` 用在浮点上（含 `2.0`）按无格式写（旧版对整数值的浮点补零）。迁移：要固定写法的值先在调用方转成字符串；需要三位以上精度时改在代码里格式化。新增 `FormatMessageIn`、`TryFormatMessageIn`、`FormatArgumentIn`，`Catalog.Resolve` 按命中模板的区域写小数点与分组符（旧版一律按不变文化），依赖旧文本的地方要改。
- 调用器的截止时间、解码与异常翻译改为管道里的内置步骤（RFC-0011 规则 10，ADR-0236，撤销 ADR-0235 的「在第一个中间件之前核对截止时间」）：截止时间已到时 Ingress 与 Observe 阶段的中间件照常执行，到 Resilience 阶段才以 `deadline_before_start` 结束（旧版一个中间件都不执行）；载荷在 Validate 阶段才解码，排在它之前的中间件（认证、租户、授权）的失败先于载荷不合法报出（旧版在处理器之前解码，载荷不合法总是最后才发现，但认证失败的调用也会先解码）；Ingress 阶段与 Observe 阶段 `Beginning` 上归属排在 `xihan.kernel.core` 之前的中间件 panic 原样交给调用方（旧版一律翻译）。管道里不能再登记名为 `ObserveMiddleware`、`DeadlineMiddleware`、`ValidationMiddleware` 的中间件。迁移：需要在截止时间核对之前做的事放在 Ingress 或 Observe 阶段；要被翻译 panic 的中间件放在 Observe 阶段 `End` 或之后；用到原始请求对象的中间件放在 Validate 阶段之后。
- 清单可选的字符串改为指针（ADR-0236 的补充）：`Manifest.Package`、`Manifest.Entry` 与 `ConfigContribution.Schema` 由 `string` 改为 `*string`，分得清「写了空串」与「没写」，写了空串时 `Write` 照写 `"package": ""`，与 .NET 一致（旧版一律省略空串）。迁移：赋值改为 `Package: manifest.Text("XiHan.Orders")`，读取改为先判 `nil` 再解引用；比较两份清单时比较指向的值。
- 管道配置错误的消息改为 .NET 的英文原文（RFC-0004 规则 8，ADR-0236 的补充），各语言逐字相同：如 `Middleware ordering constraints in stage 'validate' form a cycle: 'Guard[a]', 'Guard[b]'.`（成环时按基准次序列出全部仍被卡住的中间件，按名字登记的实例写成 `类型名[名字]`；旧版只写阶段）、`The ordering constraint of middleware 'Guard[a]' references unregistered middleware 'zz'.`、`Unknown pipeline stage 'bogus'.`。约束目标改为逐阶段解析，前一阶段成环先于后一阶段的约束目标未登记报出（旧版先解析全部约束）。错误码不变。迁移：按消息文本匹配的地方改为按错误码匹配。

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
