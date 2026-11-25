// Package plugin 是进程外插件的运行时（RFC-0017，spec/plugin/）：读取宿主下发的环境变量、以 gRPC 服务端提供
// xihan.plugin.v1 的 Plugin 服务、向 stdout 写握手行、按 lifecycle.md §7 维护调用次序、生命周期状态机与健康推送、
// 把调用分派给登记的操作，并以 Host 客户端回调宿主；Stop、stdin 关闭与 SIGTERM 都按规范的时限收尾退出。
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// 协议常量（spec/plugin/handshake.md）。
const (
	// ProtocolVersion 是本 SDK 实现的协议版本。
	ProtocolVersion uint32 = 1
	// HandshakeLineVersion 是握手行格式版本。
	HandshakeLineVersion = "xihan-plugin/1"
	// SessionMetadataKey 是携带会话令牌的元数据键：宿主调用插件、插件回调宿主的每次 RPC 都带它。
	SessionMetadataKey = "xihan-plugin-session"
)

// 宿主下发的环境变量。
const (
	EnvProtocolVersions = "XIHAN_PLUGIN_PROTOCOL_VERSIONS"
	EnvPrimitives       = "XIHAN_PLUGIN_PRIMITIVES"
	EnvSession          = "XIHAN_PLUGIN_SESSION"
	EnvHostAddress      = "XIHAN_PLUGIN_HOST_ADDRESS"
	EnvInstance         = "XIHAN_PLUGIN_INSTANCE"
	EnvLocale           = "XIHAN_PLUGIN_LOCALE"
)

// 插件协议的错误码（conventions/error-codes.md、invocation.md §8）。
const (
	// ErrorCodeUnavailable：插件不在可服务状态，或回调连不上宿主、没有可用的回调端点。
	ErrorCodeUnavailable = "PLUGINS_UNAVAILABLE_001"
	// ErrorCodeCallbackDenied：回调的会话令牌无效、所需权限未声明或未授予，或带了不属于在途调用的租户。
	ErrorCodeCallbackDenied = "PLUGINS_DENIED_002"
	// ErrorCodeUnexpected：生命周期或健康回调失败、配置节不是 JSON 对象、对方写来的错误不合法、回调没有对应的 gRPC 状态。
	ErrorCodeUnexpected = "PLUGINS_UNEXPECTED_001"
)

// SDK 运行时与宿主共用的消息键（invocation.md §8，conformance/vectors/plugin/plugin-messages.json）：错误的 details 就是模板的参数，
// 不另加成员；参数 id 一律是插件的清单 ID。目录随 SDK（RuntimeCatalog），由宿主提供，不随握手发送。
const (
	// MessageUnavailable 是插件不在可服务状态（Ready、Degraded）时到达的调用与投递，参数 {id, state}，state 是状态名。
	MessageUnavailable = "plugins.process.unavailable"
	// MessageUnexpected 是插件一侧的失败，参数 {id, phase, reason}；phase 小写（configure、start、stop、health、invoke、publish 或回调的操作 ID）。
	MessageUnexpected = "plugins.process.failed"
	// MessageUnsubscribed 是投递找不到订阅（错误码 KERNEL_NOTFOUND_001），参数 {id, topic, type, mode}，空方式写 competing。
	MessageUnsubscribed = "plugins.event.unsubscribed"
	// MessageHostUnavailable 是回调连不上宿主或没有可用的回调端点，参数 {id, reason}。
	MessageHostUnavailable = "plugins.host.unavailable"
	// MessageCallbackDenied 是回调所需的权限未在清单声明或未被授予，参数 {id, permission}。
	MessageCallbackDenied = "plugins.callback.denied"
	// MessageTenantDenied 是回调带了不属于在途调用或投递的租户，参数 {id, tenant}。
	MessageTenantDenied = "plugins.callback.tenant_denied"
	// MessageSessionInvalid 是宿主拒绝了回调的会话令牌，没有参数。
	MessageSessionInvalid = "plugins.callback.session_invalid"
)

// Network 是端点的网络类型。
type Network string

// 端点网络类型。
const (
	NetworkUnix  Network = "unix"
	NetworkTCP   Network = "tcp"
	NetworkPipe  Network = "npipe"
	networkUnset Network = ""
)

// SubscriptionMode 是订阅方式（RFC-0009「订阅方式」）：宿主按它向消息代理订阅主题。
type SubscriptionMode string

// 订阅方式。
const (
	// SubscriptionModeCompeting 是竞争消费：订阅了主题的宿主实例里只有一个把消息交给插件；空同它。
	SubscriptionModeCompeting SubscriptionMode = "competing"
	// SubscriptionModeBroadcast 是广播：每个在线的宿主实例都把消息交给自己的插件实例，尽力而为、不进死信、不回放历史。
	SubscriptionModeBroadcast SubscriptionMode = "broadcast"
)

// orCompeting 把空的订阅方式当作竞争消费：声明与投递里的空都同 competing。
func (m SubscriptionMode) orCompeting() SubscriptionMode {
	if m == "" {
		return SubscriptionModeCompeting
	}
	return m
}

// Subscription 是一个事件订阅：主题，可选的只投递的 CloudEvents type，以及订阅方式。
// 同一（Topic，MessageType）对只能声明一次，不能以两种方式各声明一次（handshake.md §3）；握手按声明的顺序写出，空方式写 competing。
type Subscription struct {
	Topic       string
	MessageType string
	// Mode 是订阅方式；空同 SubscriptionModeCompeting。
	Mode SubscriptionMode
}

// Envelope 是 CloudEvents 1.0 信封的平铺（RFC-0009）。
type Envelope struct {
	Topic           string
	ID              string
	Source          string
	Type            string
	SpecVersion     string
	Subject         string
	Time            string
	DataContentType string
	Data            []byte
	Extensions      map[string]string
}

// EventHandler 处理一条投递的消息；返回失败由宿主的消息代理按重试与死信策略处理，处理器必须幂等。
// 一次投递匹配多个订阅时按声明顺序依次交给各自的处理器，第一个失败即结果；处理器 panic 为 KERNEL_UNEXPECTED_001（invocation.md §4）。
// 这次投递的订阅方式不在参数里，用 EventDeliveryOf(ctx) 取。
type EventHandler func(ctx *invocation.Context, envelope Envelope, attempt uint32) primitives.Result[primitives.Unit]

// EventDelivery 是一次投递的附加信息（HandleEventRequest 的 attempt 与 mode，invocation.md §4）。
type EventDelivery struct {
	// Attempt 是投递次数，从 1 起；与处理器的 attempt 参数相同。
	Attempt uint32
	// Mode 是这次投递的订阅方式：SubscriptionModeCompeting 或 SubscriptionModeBroadcast，宿主写空时是 competing。
	// 同一个处理器函数配给两种方式的订阅时靠它分辨：广播投递尽力而为、不进死信、不回放历史（RFC-0009「订阅方式」）。
	Mode SubscriptionMode
}

// deliveryKey 是调用上下文对象袋里放 EventDelivery 的键。
type deliveryKey struct{}

// EventDeliveryOf 从调用上下文取这次投递的附加信息：运行时在每次投递的上下文里放入它，事件处理器收到的上下文及其派生上下文上都取得到；
// 操作调用的上下文与不是运行时建的上下文上为假。
func EventDeliveryOf(ctx *invocation.Context) (EventDelivery, bool) {
	value, ok := ctx.Get(deliveryKey{})
	if !ok {
		return EventDelivery{}, false
	}
	delivery, ok := value.(EventDelivery)
	return delivery, ok
}

// SubscriptionHandler 把订阅与处理器配对。
type SubscriptionHandler struct {
	Subscription Subscription
	Handler      EventHandler
}

// HealthStatus 是健康项状态。
type HealthStatus int

// 健康项状态。
const (
	Healthy HealthStatus = iota + 1
	DegradedHealth
	Unhealthy
)

// HealthItem 是一个健康项；名字须在清单 contributes.health 里声明。
type HealthItem struct {
	Name        string
	Status      HealthStatus
	Description string
}

// Health 是插件的健康。健康回调报的状态只有 Ready、Degraded、Failed 会改变插件的状态（lifecycle.md §7.4），其余照旧；
// Start 之前的 Health 报本地状态（Declared、Resolved、Configured）、没有健康项。
type Health struct {
	State primitives.LifecycleState
	Items []HealthItem
	Error *primitives.Error
}

// Binding 是宿主解析后的资源绑定（lifecycle.md §2、§7.2，ADR-0285）。主值与属性值可能是凭据，不要写进日志、错误与健康条目：
// 它的调试输出（fmt 的各种动词与 slog）与 encoding/json 的写出只带绑定名、类型与属性名，不带 Value 与属性值，放在切片、
// Configuration 与别的结构里一样；不经这些出口的写出不在此列（按字段反射的其他编码器、放在未导出字段里再交给 fmt 等），
// 照样带全部字段。
type Binding struct {
	// Name 是清单 bindings 里声明的名字。
	Name string
	// Type 是资源类型（connection:relational、cache:distributed、queue、blob 等）。
	Type string
	// Value 是主值（连接串或地址）；来源只给了属性、没有主值时为空，这不是错误，运行时也不拿属性拼出一个主值。
	Value string
	// Properties 是绑定的其余属性：属性名到属性值，与宿主进程里的特性读到的相同。属性名区分大小写，运行时不折叠大小写、不改写，
	// 也不核对文法与上限（那是宿主的事）；值为空的属性照样在表里；各项的先后没有含义。运行时交出的绑定没有属性时
	// （含按较早的规范实现的宿主不发这个字段）是空表、不是 nil，表是这次配置自己的一份，改它不影响别处。
	Properties map[string]string
}

// propertyNames 返回属性名，按升序：调试输出只带名字，次序固定。
func (b Binding) propertyNames() []string {
	names := make([]string, 0, len(b.Properties))
	for name := range b.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// String 返回调试文本：绑定名、类型与按升序排列的属性名，不带主值与属性值（lifecycle.md §7.2）。
func (b Binding) String() string {
	return fmt.Sprintf("Binding{Name: %q, Type: %q, Properties: %q}", b.Name, b.Type, b.propertyNames())
}

// GoString 是 %#v 的输出：与 String 带同样的内容，不带主值与属性值，所以不是能还原出绑定的 Go 字面量。
func (b Binding) GoString() string {
	return fmt.Sprintf("plugin.Binding{Name: %q, Type: %q, Properties: %q}", b.Name, b.Type, b.propertyNames())
}

// Format 实现 fmt.Formatter，接管所有动词：不实现它时 %d 之类不经 String 的动词会按字段打印出主值与属性值。
// %#v 写 GoString；%v、%+v、%s 按原来的标志、宽度与精度把 String 当字符串格式化，%q、%x、%X 同样作用在 String 上；
// 其余动词写成 fmt 报错动词的样子，同样只带 String。%p 例外：fmt 先于任何方法处理它，用在不是指针的绑定上时按字段打印（go vet 会报这种写法）。
func (b Binding) Format(state fmt.State, verb rune) {
	switch verb {
	case 'v':
		if state.Flag('#') {
			_, _ = io.WriteString(state, b.GoString())
			return
		}
		// %v 与 %+v 按 %s 格式化：加号对字符串没有含义，宽度与减号照常起作用
		_, _ = fmt.Fprintf(state, fmt.FormatString(state, 's'), b.String())
	case 's', 'q', 'x', 'X':
		_, _ = fmt.Fprintf(state, fmt.FormatString(state, verb), b.String())
	default:
		_, _ = fmt.Fprintf(state, "%%!%c(plugin.Binding=%s)", verb, b.String())
	}
}

// LogValue 实现 slog.LogValuer：日志里的绑定是一个组，只有 name、type 与 propertyNames（按升序排列的属性名），不带主值与属性值；
// 成员与 MarshalJSON 相同，JSON 行处理器写单个绑定与写切片、Configuration 里的绑定是同一个样子。
func (b Binding) LogValue() slog.Value {
	return slog.GroupValue(slog.String("name", b.Name), slog.String("type", b.Type), slog.Any("propertyNames", b.propertyNames()))
}

// bindingJSON 是绑定在 JSON 里的样子：与 TypeScript SDK 的 toJSON 同样的成员与先后，属性名没有时是空数组、不是 null。
type bindingJSON struct {
	// Name 是绑定名。
	Name string `json:"name"`
	// Type 是资源类型。
	Type string `json:"type"`
	// PropertyNames 是按升序排列的属性名。
	PropertyNames []string `json:"propertyNames"`
}

// MarshalJSON 实现 json.Marshaler：写出 {"name":…,"type":…,"propertyNames":[…]}，属性名按升序，不带主值与属性值（lifecycle.md §7.2）；
// 值接收者，绑定、绑定的指针、[]Binding 与 Configuration 经 encoding/json 都走这里，JSON 日志处理器写切片与配置时也是。
// 没有对应的 UnmarshalJSON：写出的文本少了主值与属性值，读不回原来的绑定，这是有意的。
func (b Binding) MarshalJSON() ([]byte, error) {
	return json.Marshal(bindingJSON{Name: b.Name, Type: b.Type, PropertyNames: b.propertyNames()})
}

// Configuration 是 Configure 下发的内容。
type Configuration struct {
	// Section 是清单 contributes.config.section 节的 JSON 对象原文；宿主写空时为 {}。不是 JSON 对象的配置节不会交到作者手里（lifecycle.md §7.2）。
	Section json.RawMessage
	// GrantedPermissions 是宿主授予的权限；回调前的本地核对以它为准（invocation.md §5）。
	GrantedPermissions []string
	// Bindings 只含清单声明的绑定，按宿主下发的先后，每个连同它的属性（Binding.Properties）。
	Bindings []Binding
	// WorkDirectory 是宿主分配的可写工作目录。
	WorkDirectory string
}

// Hooks 是插件作者接入生命周期的回调；都可为空。返回的错误是（或用 %w 包着）*primitives.Error 时它原样作为 Status.error 回给宿主；返回别的 error 或 panic
// 算作回调抛出（lifecycle.md §7.6）：PLUGINS_UNEXPECTED_001 + plugins.process.failed，reason 是错误的动态类型名（去掉包名与 *，
// 如 *fs.PathError 写 PathError），panic 写 panic，错误的文本与 panic 的值只写进 stderr 日志。
type Hooks struct {
	// Configure 在 Configure 时调用，配置节已核对是 JSON 对象；失败即插件 Failed。
	Configure func(ctx context.Context, configuration Configuration) error
	// Start 在 Start 时调用；成功后运行时立即求一次 Health，按结果进入 Ready、Degraded 或 Failed，然后才应答宿主（lifecycle.md §7.3）。
	Start func(ctx context.Context) error
	// Stop 在进程里最多调一次，只在 Start 成功过时调用：Stop 时在途调用已经结束或已触发取消（lifecycle.md §7.5），reason 原样是宿主给的；
	// stdin 关闭或收到 SIGTERM 时 reason 为 shutdown（§7.7）。grace 是宽限里剩下的时间，失败只记录，进程照常退出。
	Stop func(ctx context.Context, reason string, grace time.Duration) error
	// Health 给出当前健康；为空时按运行时自己的状态上报且没有健康项。只在 Start 时与 Ready、Degraded 期间调用：每次 Health、
	// WatchHealth 开始时与之后每 10 秒各一次；健康项名不在清单 contributes.health 里的丢掉。
	Health func() Health
}

// Catalog 是一份消息目录：目录名 + 区域 → 键 → 模板（RFC-0010）。
type Catalog struct {
	Name    string
	Locales map[string]map[string]string
}

// Options 是 Serve 的输入。
type Options struct {
	// Manifest 是 xihan.manifest.json 的原文；握手时原样发送，所以必须与插件目录里的文件一致。
	Manifest []byte
	// Operations 是插件服务的操作；每个 ID 必须出现在清单 contributes.operations 里。
	Operations *operation.Registry
	// Pipeline 是插件内的管道；为空时只有调用器的三个内置步骤（Observe 阶段翻译 panic、Resilience 阶段核对截止时间、
	// Validate 阶段解码载荷，RFC-0011 规则 10）与 handle 阶段之后的操作分派，给了也照样加上这三步。
	Pipeline *pipeline.Builder
	// Subscriptions 是事件订阅与处理器。
	Subscriptions []SubscriptionHandler
	// Catalogs 是插件自己的消息目录，握手时发给宿主；每份都要含 en-US。SDK 运行时的目录（RuntimeCatalog）由宿主提供，不要放进来。
	Catalogs []Catalog
	// Hooks 是生命周期回调。
	Hooks Hooks
	// Network 是监听的网络类型；空为 unix（Windows 10 1803 起也支持），临时目录的路径含空白时改用 tcp（握手行的地址不能含空白，handshake.md §2）。
	Network Network
	// Address 是监听地址，不能含空白；空时自动生成（unix 为临时目录下新建的私有目录（0700，停止后删除）里的套接字，
	// tcp 为 127.0.0.1 的随机端口，npipe 为随机管道名）。tcp 只接受 127.0.0.1:<端口>。
	Address string
	// Stdin 是宿主保持打开的输入；读到结尾即收尾退出（lifecycle.md §7.7）。空为 os.Stdin。
	Stdin io.Reader
	// Stdout 只用于写握手行。空为 os.Stdout。
	Stdout io.Writer
	// Stderr 接收运行时日志（JSON 行）。空为 os.Stderr。给了 Logger 时不用它。
	Stderr io.Writer
	// Logger 是运行时的日志器：运行时自己的日志（加上属性 plugin）与调用器的日志（操作 panic 的值与堆栈、响应写不出的原因）都写进它。
	// 空时自建一个把 JSON 行写到 Stderr 的（NewLogHandler，级别 Info）。自己给的要让宿主读得懂，用 NewLogHandler 做处理器、写到进程的 stderr
	// （invocation.md §6）；处理器里的操作日志想进同一处就用同一个日志器。
	Logger *slog.Logger
	// Getenv 读取环境变量；空为 os.Getenv。
	Getenv func(string) string
}
