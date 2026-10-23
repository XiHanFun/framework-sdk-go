// Package invocation 是调用上下文（RFC-0003）：取消、截止时间、W3C 追踪、已登记的 Baggage 与请求作用域对象袋；
// 不绑定任何协议，载体映射由协议适配器与插件运行时实现。
package invocation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// 内核登记的、与上下文有关的错误码。
const (
	// ErrorCodeDeadlineExceeded：调用超过截止时间。
	ErrorCodeDeadlineExceeded = "KERNEL_TIMEOUT_001"
	// ErrorCodeCancelled：调用被取消。
	ErrorCodeCancelled = "KERNEL_CANCELLED_001"
)

// 与上下文有关的消息键。
const (
	// MessageDeadlineExceeded 是超时的消息键。
	MessageDeadlineExceeded = "kernel.invocation.deadline_exceeded"
	// MessageCancelled 是取消的消息键。
	MessageCancelled = "kernel.invocation.cancelled"
)

// traceParentPattern 是 W3C traceparent：version-traceid-spanid-flags。
var traceParentPattern = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

// Context 是一次调用的上下文；嵌入的 context.Context 承载取消与截止时间，所以可以直接传给需要 context 的 API。
// 派生上下文共享对象袋，不共享 Baggage（按值复制）。
type Context struct {
	context.Context
	deadline time.Time
	traceID  string
	spanID   string
	sampled  bool
	baggage  map[string]string
	items    *items
}

// items 是派生上下文共享的请求作用域对象袋。
type items struct {
	mu     sync.RWMutex
	values map[any]any
}

// Option 配置新建的上下文。
type Option func(*Context)

// WithDeadline 设置绝对截止时间（UTC）；零值表示没有。
func WithDeadline(deadline time.Time) Option {
	return func(c *Context) { c.deadline = deadline.UTC() }
}

// WithTimeout 以相对时间设置截止时间。
func WithTimeout(timeout time.Duration) Option {
	return func(c *Context) { c.deadline = time.Now().UTC().Add(timeout) }
}

// WithTrace 设置追踪 ID（32 位小写十六进制）与 Span ID（16 位小写十六进制）。
func WithTrace(traceID, spanID string) Option {
	return func(c *Context) {
		c.traceID = traceID
		c.spanID = spanID
	}
}

// WithTraceParent 从 W3C traceparent 设置追踪；不合法时忽略。
func WithTraceParent(traceParent string) Option {
	return func(c *Context) {
		if traceID, spanID, sampled, ok := ParseTraceParent(traceParent); ok {
			c.traceID, c.spanID, c.sampled = traceID, spanID, sampled
		}
	}
}

// WithBaggage 设置多个 Baggage 键值；键必须符合 xihan.<name>，不合法的键是编程错误。
func WithBaggage(baggage map[string]string) Option {
	return func(c *Context) {
		for key, value := range baggage {
			mustBaggageKey(key)
			c.baggage[key] = value
		}
	}
}

// New 创建上下文：没有追踪时生成新的 Trace ID 与 Span ID；parent 为 nil 时用 context.Background()。
func New(parent context.Context, options ...Option) *Context {
	if parent == nil {
		parent = context.Background()
	}
	c := &Context{Context: parent, baggage: map[string]string{}, items: &items{values: map[any]any{}}, sampled: true}
	for _, option := range options {
		option(c)
	}
	if c.traceID == "" {
		c.traceID = NewTraceID()
	}
	if c.spanID == "" {
		c.spanID = NewSpanID()
	}
	if !c.deadline.IsZero() {
		// 截止时间与取消并存：到达截止时间时取消令牌触发（RFC-0003 规则 1）
		ctx, cancel := context.WithDeadline(parent, c.deadline)
		c.Context = ctx
		context.AfterFunc(parent, cancel)
	}
	return c
}

// Background 创建没有父取消的上下文。
func Background(options ...Option) *Context { return New(context.Background(), options...) }

// Deadline 返回截止时间；没有时第二个返回值为假。实现 context.Context。
func (c *Context) Deadline() (time.Time, bool) {
	if c.deadline.IsZero() {
		return c.Context.Deadline()
	}
	return c.deadline, true
}

// HasDeadline 判断是否有截止时间。
func (c *Context) HasDeadline() bool { return !c.deadline.IsZero() }

// IsExpired 判断是否已过截止时间。
func (c *Context) IsExpired(now time.Time) bool {
	return !c.deadline.IsZero() && !now.Before(c.deadline)
}

// RemainingTime 返回到截止时间的剩余时间；没有截止时间时第二个返回值为假，已过期为零。
func (c *Context) RemainingTime(now time.Time) (time.Duration, bool) {
	if c.deadline.IsZero() {
		return 0, false
	}
	remaining := c.deadline.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

// TraceID 返回追踪 ID。
func (c *Context) TraceID() string { return c.traceID }

// SpanID 返回 Span ID。
func (c *Context) SpanID() string { return c.spanID }

// TraceParent 返回 W3C traceparent。
func (c *Context) TraceParent() string {
	flags := "00"
	if c.sampled {
		flags = "01"
	}
	return "00-" + c.traceID + "-" + c.spanID + "-" + flags
}

// Baggage 返回 Baggage 的副本。
func (c *Context) Baggage() map[string]string {
	copied := make(map[string]string, len(c.baggage))
	for key, value := range c.baggage {
		copied[key] = value
	}
	return copied
}

// GetBaggage 取一个 Baggage 值。
func (c *Context) GetBaggage(key string) (string, bool) {
	value, ok := c.baggage[key]
	return value, ok
}

// WithBaggage 派生一个多一个 Baggage 键值的上下文；键必须符合 xihan.<name>。
func (c *Context) WithBaggage(key, value string) *Context {
	mustBaggageKey(key)
	derived := *c
	derived.baggage = c.Baggage()
	derived.baggage[key] = value
	return &derived
}

// WithoutUnregisteredBaggage 派生一个只保留已登记键的上下文（Egress 阶段剥离未登记键，RFC-0003 规则 3）。
func (c *Context) WithoutUnregisteredBaggage() *Context {
	derived := *c
	derived.baggage = map[string]string{}
	for key, value := range c.baggage {
		if primitives.IsBaggageKeyRegistered(key) {
			derived.baggage[key] = value
		}
	}
	return &derived
}

// Tenant 返回当前租户 ID；没有为空。
func (c *Context) Tenant() string { return c.baggage[primitives.BaggageTenant] }

// Principal 返回当前主体 ID；没有为空。
func (c *Context) Principal() string { return c.baggage[primitives.BaggagePrincipal] }

// Correlation 返回业务关联 ID；没有为空。
func (c *Context) Correlation() string { return c.baggage[primitives.BaggageCorrelation] }

// IdempotencyKey 返回幂等键；没有为空。
func (c *Context) IdempotencyKey() string { return c.baggage[primitives.BaggageIdempotencyKey] }

// Locale 返回请求文化（BCP 47）；没有为空。
func (c *Context) Locale() string { return c.baggage[primitives.BaggageLocale] }

// Set 在对象袋里放一个值；派生上下文共享对象袋，值不跨进程传播。
func (c *Context) Set(key, value any) {
	c.items.mu.Lock()
	defer c.items.mu.Unlock()
	c.items.values[key] = value
}

// Get 从对象袋里取一个值。
func (c *Context) Get(key any) (any, bool) {
	c.items.mu.RLock()
	defer c.items.mu.RUnlock()
	value, ok := c.items.values[key]
	return value, ok
}

// Cancellation 把取消与超时翻译成原语层错误：超过截止时间为 KERNEL_TIMEOUT_001，外部取消为 KERNEL_CANCELLED_001；
// 未取消为 nil。嵌入的 Err() 仍是 context.Context 的原始错误。
func (c *Context) Cancellation() *primitives.Error {
	return FromContextError(c.Context.Err())
}

// FromContextError 把 context 的错误翻译成原语层错误；nil 与其他错误返回 nil。
func FromContextError(err error) *primitives.Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return primitives.Timeout(ErrorCodeDeadlineExceeded, MessageDeadlineExceeded, nil)
	case errors.Is(err, context.Canceled):
		return primitives.Cancelled(ErrorCodeCancelled, MessageCancelled, nil)
	default:
		return nil
	}
}

// ParseTraceParent 解析 W3C traceparent，返回追踪 ID、Span ID 与采样标志。
func ParseTraceParent(traceParent string) (traceID, spanID string, sampled, ok bool) {
	match := traceParentPattern.FindStringSubmatch(traceParent)
	if match == nil || match[2] == "00000000000000000000000000000000" || match[3] == "0000000000000000" {
		return "", "", false, false
	}
	return match[2], match[3], match[4] == "01", true
}

// NewTraceID 生成 32 位小写十六进制的追踪 ID。
func NewTraceID() string { return randomHex(16) }

// NewSpanID 生成 16 位小写十六进制的 Span ID。
func NewSpanID() string { return randomHex(8) }

// randomHex 生成 n 字节随机数的十六进制。
func randomHex(n int) string {
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buffer)
}

// mustBaggageKey 校验 Baggage 键，不合法即 panic（编程错误）。
func mustBaggageKey(key string) {
	if !primitives.IsBaggageKeyWellFormed(key) {
		panic(&primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: fmt.Sprintf("The baggage key '%s' must match xihan.<name>.", key)})
	}
}
