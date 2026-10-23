// Package echo 是用本 SDK 写的回声插件：与 XiHan.Framework 的 samples/EchoPlugin（.NET）行为对齐，
// 既是 SDK 的端到端用例，也是 .NET 宿主加载 Go 插件的示例（P4-06）。
package echo

import (
	"context"
	"embed"
	"encoding/json"
	"log/slog"
	"os"
	"sync"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/plugin"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// Manifest 是插件清单原文；握手时原样发送，所以构建产物目录里的 xihan.manifest.json 必须是同一份。
//
//go:embed xihan.manifest.json
var Manifest []byte

// localization 内嵌消息目录。
//
//go:embed Localization
var localization embed.FS

// 插件自己的错误码与消息键。
const (
	ErrorCodeHostUnavailable = "ECHOGO_UNAVAILABLE_001"
	MessageHostUnavailable   = "echogo.host.unavailable"
	ErrorCodeTextMissing     = "ECHOGO_VALIDATION_002"
	MessageTextMissing       = "echogo.text.missing"
)

// CreateRequest 是 echoes.create 的请求。
type CreateRequest struct {
	Text string `json:"text"`
}

// CreateResponse 是 echoes.create 的响应：回声文本、配置里的问候语与上下文里的租户、主体、关联 ID。
type CreateResponse struct {
	Text        string `json:"text"`
	Greeting    string `json:"greeting,omitempty"`
	Tenant      string `json:"tenant,omitempty"`
	Principal   string `json:"principal,omitempty"`
	Correlation string `json:"correlation,omitempty"`
}

// RelayRequest 是 echoes.relay 的请求：经宿主回调调用 operation，把宿主的应答原样返回。
type RelayRequest struct {
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
}

// ListResponse 是 events.list 的响应：收到的事件 ID。
type ListResponse struct {
	IDs []string `json:"ids"`
}

// settings 是配置节 XiHan:Echo。
type settings struct {
	Greeting string `json:"greeting"`
}

// state 是插件的运行状态。
type state struct {
	mu       sync.Mutex
	greeting string
	events   []string
}

// Options 组装插件运行时的选项。
func Options() (plugin.Options, error) {
	catalog, err := plugin.LoadCatalog("echogo", localization, "Localization")
	if err != nil {
		return plugin.Options{}, err
	}
	primitives.SetDefaultMessages(catalog.Resolver(), "en-US")

	s := &state{}
	registry := operation.NewRegistry()
	if err := operation.Add(registry, operation.Contract{ID: "echoes.create", Stability: primitives.Experimental, Expose: []string{operation.ExposeHTTP, operation.ExposeMcp}}, operation.HandlerFunc[CreateRequest, CreateResponse](s.create), &operation.Schema{
		Request:  `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
		Response: `{"type":"object","properties":{"text":{"type":"string"},"greeting":{"type":"string"},"tenant":{"type":"string"},"principal":{"type":"string"},"correlation":{"type":"string"}},"required":["text"]}`,
	}, ""); err != nil {
		return plugin.Options{}, err
	}
	if err := operation.Add(registry, operation.Contract{ID: "echoes.relay", Stability: primitives.Experimental, Expose: []string{operation.ExposeHTTP}}, operation.HandlerFunc[RelayRequest, json.RawMessage](s.relay), nil, ""); err != nil {
		return plugin.Options{}, err
	}
	if err := operation.Add(registry, operation.Contract{ID: "echoes.crash", Stability: primitives.Experimental}, operation.HandlerFunc[struct{}, struct{}](s.crash), nil, ""); err != nil {
		return plugin.Options{}, err
	}
	if err := operation.Add(registry, operation.Contract{ID: "events.list", Stability: primitives.Experimental, Expose: []string{operation.ExposeHTTP}}, operation.HandlerFunc[struct{}, ListResponse](s.list), nil, ""); err != nil {
		return plugin.Options{}, err
	}

	return plugin.Options{
		Manifest:   Manifest,
		Operations: registry,
		Subscriptions: []plugin.SubscriptionHandler{{
			Subscription: plugin.Subscription{Topic: "echo.events"},
			Handler:      s.handleEvent,
		}},
		Catalogs: []plugin.Catalog{catalog},
		Hooks: plugin.Hooks{
			Configure: s.configure,
			Health:    s.health,
		},
	}, nil
}

// Serve 以进程外插件的身份运行到停止。
func Serve(ctx context.Context) error {
	opts, err := Options()
	if err != nil {
		return err
	}
	return plugin.Serve(ctx, opts)
}

// configure 读配置节 XiHan:Echo 的 greeting。
func (s *state) configure(_ context.Context, configuration plugin.Configuration) error {
	var parsed settings
	if err := configuration.Decode(&parsed); err != nil {
		return primitives.Validation("ECHOGO_VALIDATION_001", "echogo.configuration.invalid", map[string]any{"reason": err.Error()})
	}
	s.mu.Lock()
	s.greeting = parsed.Greeting
	s.mu.Unlock()
	return nil
}

// health 报告健康项 echo。
func (s *state) health() plugin.Health {
	return plugin.Health{State: primitives.Ready, Items: []plugin.HealthItem{{Name: "echo", Status: plugin.Healthy}}}
}

// create 回声文本并附上下文；文本为空是业务失败，宿主用握手时拿到的目录翻译它。
func (s *state) create(ctx *invocation.Context, request CreateRequest) primitives.Result[CreateResponse] {
	if request.Text == "" {
		return primitives.Fail[CreateResponse](primitives.Validation(ErrorCodeTextMissing, MessageTextMissing, map[string]any{"operation": "echoes.create"}))
	}
	s.mu.Lock()
	greeting := s.greeting
	s.mu.Unlock()
	return primitives.Ok(CreateResponse{Text: request.Text, Greeting: greeting, Tenant: ctx.Tenant(), Principal: ctx.Principal(), Correlation: ctx.Correlation()})
}

// relay 经宿主回调调用另一个操作并原样返回应答；没有宿主回调端点时失败。
func (s *state) relay(ctx *invocation.Context, request RelayRequest) primitives.Result[json.RawMessage] {
	host, ok := plugin.HostOf(ctx)
	if !ok {
		return primitives.Fail[json.RawMessage](primitives.Unavailable(ErrorCodeHostUnavailable, MessageHostUnavailable, nil))
	}
	response := host.Invoke(ctx, request.Operation, request.Payload)
	if !response.Outcome.OK() {
		return primitives.Fail[json.RawMessage](response.Outcome.Err())
	}
	if len(response.Payload) == 0 {
		return primitives.Ok(json.RawMessage("{}"))
	}
	return primitives.Ok(json.RawMessage(response.Payload))
}

// crash 故意让进程异常退出而不应答：宿主应记为崩溃并按退避重启。
func (s *state) crash(*invocation.Context, struct{}) primitives.Result[struct{}] {
	slog.Default().Error("crashing on request")
	os.Exit(3)
	return primitives.Ok(struct{}{})
}

// list 列出收到的事件 ID。
func (s *state) list(*invocation.Context, struct{}) primitives.Result[ListResponse] {
	s.mu.Lock()
	ids := append([]string(nil), s.events...)
	s.mu.Unlock()
	if ids == nil {
		ids = []string{}
	}
	return primitives.Ok(ListResponse{IDs: ids})
}

// handleEvent 记录投递的事件。
func (s *state) handleEvent(_ *invocation.Context, envelope plugin.Envelope, _ uint32) primitives.Result[primitives.Unit] {
	s.mu.Lock()
	s.events = append(s.events, envelope.ID)
	s.mu.Unlock()
	return primitives.Success()
}
