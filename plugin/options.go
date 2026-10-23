// Package plugin 是进程外插件的运行时（RFC-0017，spec/plugin/）：读取宿主下发的环境变量、以 gRPC 服务端提供
// xihan.plugin.v1 的 Plugin 服务、向 stdout 写握手行、维护生命周期状态机与健康推送、把调用分派给登记的操作，
// 并以 Host 客户端回调宿主；stdin 关闭即退出。
package plugin

import (
	"context"
	"encoding/json"
	"io"
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
	// SessionMetadataKey 是回调宿主时携带会话令牌的元数据键。
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

// 插件协议的错误码（conventions/error-codes.md）。
const (
	// ErrorCodeUnavailable：插件未就绪、正在停止或不可达。
	ErrorCodeUnavailable = "PLUGINS_UNAVAILABLE_001"
	// ErrorCodeCallbackDenied：回调的会话令牌无效或权限未授予。
	ErrorCodeCallbackDenied = "PLUGINS_DENIED_002"
	// ErrorCodeUnexpected：配置、启动、停止或调用时的未预期失败。
	ErrorCodeUnexpected = "PLUGINS_UNEXPECTED_001"
)

// 插件协议的消息键。
const (
	MessageUnavailable    = "plugins.process.unavailable"
	MessageCallbackDenied = "plugins.callback.denied"
	MessageUnexpected     = "plugins.process.failed"
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

// Subscription 是一个事件订阅：主题，以及可选的只投递的 CloudEvents type。
type Subscription struct {
	Topic       string
	MessageType string
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
type EventHandler func(ctx *invocation.Context, envelope Envelope, attempt uint32) primitives.Result[primitives.Unit]

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

// Health 是插件自报的健康：状态只能是 Started、Ready、Degraded、Draining、Failed。
type Health struct {
	State primitives.LifecycleState
	Items []HealthItem
	Error *primitives.Error
}

// Binding 是宿主解析后的资源绑定。
type Binding struct {
	Name  string
	Type  string
	Value string
}

// Configuration 是 Configure 下发的内容。
type Configuration struct {
	// Section 是清单 contributes.config.section 节的 JSON 对象；没有声明配置节时为 {}。
	Section json.RawMessage
	// GrantedPermissions 等于清单 permissions。
	GrantedPermissions []string
	// Bindings 只含清单声明的绑定。
	Bindings []Binding
	// WorkDirectory 是宿主分配的可写工作目录。
	WorkDirectory string
}

// Hooks 是插件作者接入生命周期的回调；都可为空。返回的错误会作为 Status.error 回给宿主（*primitives.Error 原样，其他错误转为 PLUGINS_UNEXPECTED_001）。
type Hooks struct {
	// Configure 在 Configure 时调用；失败即插件 Failed。
	Configure func(ctx context.Context, configuration Configuration) error
	// Start 在 Start 时调用；返回后插件接受调用。
	Start func(ctx context.Context) error
	// Stop 在 Stop 时调用：完成在途工作并释放资源；grace 是宿主给的宽限。
	Stop func(ctx context.Context, reason string, grace time.Duration) error
	// Health 给出当前健康；为空时按运行时自己的状态上报且没有健康项。
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
	// Pipeline 是插件内的管道；为空时只有 handle 阶段。
	Pipeline *pipeline.Builder
	// Subscriptions 是事件订阅与处理器。
	Subscriptions []SubscriptionHandler
	// Catalogs 是消息目录；至少应含 en-US。
	Catalogs []Catalog
	// Hooks 是生命周期回调。
	Hooks Hooks
	// Network 是监听的网络类型；空为 unix（Windows 10 1803 起也支持）。
	Network Network
	// Address 是监听地址；空时自动生成（unix 为临时目录里的套接字，tcp 为 127.0.0.1 的随机端口，npipe 为随机管道名）。
	Address string
	// Stdin 是宿主保持打开的输入；关闭即退出。空为 os.Stdin。
	Stdin io.Reader
	// Stdout 只用于写握手行。空为 os.Stdout。
	Stdout io.Writer
	// Stderr 接收运行时日志（JSON 行）。空为 os.Stderr。
	Stderr io.Writer
	// Getenv 读取环境变量；空为 os.Getenv。
	Getenv func(string) string
	// StopGrace 是没有宿主宽限时等待在途调用的时间；零为 5 秒。
	StopGrace time.Duration
}
