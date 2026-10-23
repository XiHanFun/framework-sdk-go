// Package caching 是 caching.distributed@1 远程 Provider 的插件侧（RFC-0019，spec/provider/caching-distributed.md）：
// Store 是 RFC-0006 的五个原子存储操作加三个批量操作（conventions/batch-first.md），MemoryStore 是有界的进程内实现，
// RegisterOperations 把任何 Store 暴露成操作集，让 .NET 宿主的 Caching.Remote 适配器把这个插件当作分布式缓存 Provider。
package caching

import (
	"context"
	"time"
	"unicode"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// Capability 是本包实现的能力。
const Capability = "caching.distributed@1"

// 操作集的操作 ID（spec/provider/caching-distributed.md §1）。
const (
	OperationGet        = "cache-entries.get"
	OperationSet        = "cache-entries.set"
	OperationTryAdd     = "cache-entries.try-add"
	OperationRemove     = "cache-entries.remove"
	OperationTryRemove  = "cache-entries.try-remove"
	OperationGetMany    = "cache-entries.get-many"
	OperationSetMany    = "cache-entries.set-many"
	OperationRemoveMany = "cache-entries.remove-many"
)

// Operations 是操作集的全部操作，按规范顺序。
var Operations = []string{OperationGet, OperationSet, OperationTryAdd, OperationRemove, OperationTryRemove, OperationGetMany, OperationSetMany, OperationRemoveMany}

// 缓存子系统的错误码（conventions/error-codes.md）。
const (
	// ErrorCodeInvalidKey：缓存键不合法。
	ErrorCodeInvalidKey = "CACHING_VALIDATION_001"
	// ErrorCodeInvalidOptions：过期选项不合法。
	ErrorCodeInvalidOptions = "CACHING_VALIDATION_002"
	// ErrorCodeUnavailable：后端不可用或失败。
	ErrorCodeUnavailable = "CACHING_UNAVAILABLE_001"
)

// 本包的消息键；目录名 CatalogName，文案在 Localization/<区域>.json。
const (
	CatalogName            = "caching.remote"
	MessageInvalidKey      = "caching.remote.key_invalid"
	MessageInvalidOptions  = "caching.remote.options_invalid"
	MessageUnavailable     = "caching.remote.store_unavailable"
	MessageCapacityReached = "caching.remote.capacity_reached"
)

// MaxKeyLength 是键的最大长度。
const MaxKeyLength = 512

// Options 是条目的过期选项：绝对存活或滑动过期，至多一个；都为零用存储的默认存活时间。
type Options struct {
	TimeToLive time.Duration
	Sliding    time.Duration
}

// Validate 校验选项：负数或同时设置都不合法。
func (o Options) Validate() *primitives.Error {
	if o.TimeToLive < 0 || o.Sliding < 0 {
		return primitives.Validation(ErrorCodeInvalidOptions, MessageInvalidOptions, map[string]any{"reason": "expirations must be positive"})
	}
	if o.TimeToLive > 0 && o.Sliding > 0 {
		return primitives.Validation(ErrorCodeInvalidOptions, MessageInvalidOptions, map[string]any{"reason": "ttl and sliding expiration are exclusive"})
	}
	return nil
}

// Entry 是批量写入的一项：键、值与过期选项。
type Entry struct {
	Key     string
	Value   []byte
	Options Options
}

// Lookup 是批量读取的一项结果：未命中时 Found 为假、Value 为 nil。
type Lookup struct {
	Value []byte
	Found bool
}

// IsKey 判断键是否合法：非空、不含空白与控制字符、不超过 MaxKeyLength。
func IsKey(key string) bool {
	if key == "" || len(key) > MaxKeyLength {
		return false
	}
	for _, char := range key {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

// ValidateKey 校验键；不合法返回 CACHING_VALIDATION_001。
func ValidateKey(key string) *primitives.Error {
	if !IsKey(key) {
		return primitives.Validation(ErrorCodeInvalidKey, MessageInvalidKey, map[string]any{"key": key})
	}
	return nil
}

// ValidateKeys 逐个校验键；第一个不合法的键带上它在集合里的下标返回。
func ValidateKeys(keys []string) *primitives.Error {
	for index, key := range keys {
		if err := ValidateKey(key); err != nil {
			return withIndex(err, index)
		}
	}
	return nil
}

// Store 是 RFC-0006 的字节级存储契约。返回的错误应是 *primitives.Error；其他错误由操作集按后端不可用处理。
// 批量方法按 conventions/batch-first.md：一次完成、结果与入参等长且顺序一致、空集合不触后端、基础设施失败整批报错。
type Store interface {
	// Get 读取；不存在或已过期时第二个返回值为假。滑动过期的条目在读取时延长。
	Get(ctx context.Context, key string) ([]byte, bool, error)
	// Set 写入或覆盖。
	Set(ctx context.Context, key string, value []byte, options Options) error
	// TryAdd 不存在（或已过期）才写入；写入返回真。原子。
	TryAdd(ctx context.Context, key string, value []byte, options Options) (bool, error)
	// Remove 删除；存在返回真。
	Remove(ctx context.Context, key string) (bool, error)
	// TryRemove 当前值等于期望值时才删除；删除返回真。原子。
	TryRemove(ctx context.Context, key string, expected []byte) (bool, error)
	// GetMany 逐键同 Get，一次完成；命中的滑动过期条目逐个延长。
	GetMany(ctx context.Context, keys []string) ([]Lookup, error)
	// SetMany 逐项同 Set，一次完成；同一键出现多次后者覆盖。任一项不合法或容量不足时整批不写。
	SetMany(ctx context.Context, entries []Entry) error
	// RemoveMany 逐键同 Remove，一次完成；同一键出现多次第二次为假。
	RemoveMany(ctx context.Context, keys []string) ([]bool, error)
}

// Unavailable 创建后端不可用的错误。
func Unavailable(reason string) *primitives.Error {
	return primitives.Unavailable(ErrorCodeUnavailable, MessageUnavailable, map[string]any{"reason": reason})
}

// withIndex 给校验错误补上出错项在批量入参里的下标。
func withIndex(err *primitives.Error, index int) *primitives.Error {
	details := make(map[string]any, len(err.Details)+1)
	for name, value := range err.Details {
		details[name] = value
	}
	details["index"] = index
	err.Details = details
	return err
}
