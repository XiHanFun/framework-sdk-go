// Package cache 是 caching.distributed@1 的远程 Provider 示例插件：用 caching.MemoryStore 实现操作集，
// .NET 宿主经 Caching.Remote 适配器把它当分布式缓存 Provider（P4-07）。换 Redis 等后端只需换一个 caching.Store 实现。
package cache

import (
	"context"
	"embed"
	"encoding/json"
	"time"

	"github.com/xihanfun/framework-sdk-go/caching"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/plugin"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// Manifest 是插件清单原文；构建产物目录里的 xihan.manifest.json 必须是同一份。
//
//go:embed xihan.manifest.json
var Manifest []byte

// localization 内嵌 caching 包的消息目录。
//
//go:embed Localization
var localization embed.FS

// settings 是配置节 XiHan:Cache。
type settings struct {
	DefaultTimeToLiveSeconds int64 `json:"defaultTimeToLiveSeconds"`
	MaxEntries               int   `json:"maxEntries"`
}

// Options 组装插件运行时的选项；store 在 Configure 时按配置重建。
func Options() (plugin.Options, error) {
	catalog, err := plugin.LoadCatalog(caching.CatalogName, localization, "Localization")
	if err != nil {
		return plugin.Options{}, err
	}
	primitives.SetDefaultMessages(catalog.Resolver(), "en-US")

	holder := &storeHolder{store: caching.NewMemoryStore()}
	registry := operation.NewRegistry()
	if err := caching.RegisterOperations(registry, holder); err != nil {
		return plugin.Options{}, err
	}
	return plugin.Options{
		Manifest:   Manifest,
		Operations: registry,
		Catalogs:   []plugin.Catalog{catalog},
		Hooks: plugin.Hooks{
			Configure: holder.configure,
			Health: func() plugin.Health {
				return plugin.Health{State: primitives.Ready, Items: []plugin.HealthItem{{Name: "cache", Status: plugin.Healthy}}}
			},
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

// storeHolder 让 Configure 能按配置换掉存储，同时实现 caching.Store 转发。
type storeHolder struct {
	store *caching.MemoryStore
}

// configure 读配置节 XiHan:Cache 的默认存活时间与容量。
func (h *storeHolder) configure(_ context.Context, configuration plugin.Configuration) error {
	var parsed settings
	if len(configuration.Section) > 0 {
		if err := json.Unmarshal(configuration.Section, &parsed); err != nil {
			return primitives.Validation(caching.ErrorCodeInvalidOptions, caching.MessageInvalidOptions, map[string]any{"reason": "XiHan:Cache is not valid JSON: " + err.Error()})
		}
	}
	var options []caching.MemoryOption
	if parsed.DefaultTimeToLiveSeconds > 0 {
		options = append(options, caching.WithDefaultTimeToLive(time.Duration(parsed.DefaultTimeToLiveSeconds)*time.Second))
	}
	if parsed.MaxEntries > 0 {
		options = append(options, caching.WithCapacity(parsed.MaxEntries))
	}
	h.store = caching.NewMemoryStore(options...)
	return nil
}

// Get 实现 caching.Store。
func (h *storeHolder) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return h.store.Get(ctx, key)
}

// Set 实现 caching.Store。
func (h *storeHolder) Set(ctx context.Context, key string, value []byte, options caching.Options) error {
	return h.store.Set(ctx, key, value, options)
}

// TryAdd 实现 caching.Store。
func (h *storeHolder) TryAdd(ctx context.Context, key string, value []byte, options caching.Options) (bool, error) {
	return h.store.TryAdd(ctx, key, value, options)
}

// Remove 实现 caching.Store。
func (h *storeHolder) Remove(ctx context.Context, key string) (bool, error) {
	return h.store.Remove(ctx, key)
}

// TryRemove 实现 caching.Store。
func (h *storeHolder) TryRemove(ctx context.Context, key string, expected []byte) (bool, error) {
	return h.store.TryRemove(ctx, key, expected)
}
