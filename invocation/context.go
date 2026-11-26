// Package invocation 是调用上下文（RFC-0003）：取消、截止时间、W3C 追踪、已登记的 Baggage 与请求作用域对象袋；
// 不绑定任何协议，载体映射由协议适配器与插件运行时实现。
package invocation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
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

// 与上下文有关的消息键（与 .NET KernelMessages 一致，都没有参数）
const (
	// MessageDeadlineExceeded 是超时的消息键
	MessageDeadlineExceeded = "kernel.invocation.deadline_exceeded"
	// MessageDeadlineBeforeStart 是调用开始时截止时间已过的消息键
	MessageDeadlineBeforeStart = "kernel.invocation.deadline_before_start"
	// MessageCancelled 是取消的消息键
	MessageCancelled = "kernel.invocation.cancelled"
	// MessageUnhandledException 是调用里发生了未处理的 panic（.NET 的未处理异常）的消息键
	MessageUnhandledException = "kernel.invocation.unhandled_exception"
)

// 这些键的 en-US 默认模板，逐字照 .NET XiHan.Framework.Kernel/Localization/en-US.json
func init() {
	primitives.RegisterDefaultTemplates(map[string]string{
		MessageDeadlineExceeded:    "The invocation exceeded its deadline.",
		MessageDeadlineBeforeStart: "The invocation exceeded its deadline before it started.",
		MessageCancelled:           "The invocation was canceled.",
		MessageUnhandledException:  "An unhandled exception occurred during the invocation.",
	})
}

// traceParentPattern 是 W3C traceparent 版本 00 的文法（RFC-0003 规则 7）：只认版本 00 与小写十六进制，别的版本（含 ff）、大写与多余的段都不合法
var traceParentPattern = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

// Context 是一次调用的上下文；嵌入的 context.Context 承载取消与截止时间，所以可以直接传给需要 context 的 API。
// 派生上下文共享对象袋，不共享 Baggage（按值复制）。
type Context struct {
	context.Context
	deadline time.Time
	expiry   *deadlineToken
	traceID  string
	spanID   string
	// traceFlags 是上游给的采样位（0x00 或 0x01，其余位不进上下文）；hasTraceFlags 为假时没有上游，这次调用是链路的根（RFC-0003 规则 7）
	traceFlags    byte
	hasTraceFlags bool
	baggage       map[string]string
	items         *items
}

// deadlineToken 是截止令牌（RFC-0004、ADR-0092）：到达截止时间时由定时器置位，调用方的取消不停它，只有 NewWithCancel 的释放停它。
// 嵌入的 context.Context 先被取消时 Err() 停在 Canceled，截止令牌照样在截止时间置位，超时与取消的归因只看它
type deadlineToken struct {
	fired atomic.Bool
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

// WithTrace 设置追踪 ID（32 位小写十六进制）与 Span ID（16 位小写十六进制）；只给 ID 不给标志按上游已采样处理，
// 标志为 01（RFC-0003 规则 7）
func WithTrace(traceID, spanID string) Option {
	return func(c *Context) {
		c.traceID = traceID
		c.spanID = spanID
		c.traceFlags, c.hasTraceFlags = SampledFlag, true
	}
}

// WithTraceParent 从 W3C traceparent 设置追踪 ID、Span ID 与采样位：只取标志的最低位，上游「不采样」（00）原样保留；不合法时当作没给
func WithTraceParent(traceParent string) Option {
	return func(c *Context) {
		if traceID, spanID, flags, ok := parseTraceParent(traceParent); ok {
			c.traceID, c.spanID, c.traceFlags, c.hasTraceFlags = traceID, spanID, flags&SampledFlag, true
		}
	}
}

// WithBaggage 由一组 Baggage 构造上下文，是外部输入的入口（载体、插件元数据）：键必须符合 xihan.<name>，不合法的键是编程错误；
// 已登记键的值不能进入上下文时（空、超过 256 个 UTF-16 码元、含控制字符或孤立的代理项即不是合法的 UTF-8，
// primitives.IsBaggageEntryAcceptable）丢弃这个成员，原有的同名值保留，与 W3C Baggage 忽略不合法的成员一致；幂等键例外
// （conventions/baggage-keys.md，ADR-0204、ADR-0235、ADR-0321）
func WithBaggage(baggage map[string]string) Option {
	return func(c *Context) {
		for key, value := range baggage {
			mustBaggageKey(key)
			if primitives.IsBaggageEntryAcceptable(key, value) {
				c.baggage[key] = value
			}
		}
	}
}

// New 创建上下文：没有追踪时生成新的 Trace ID 与 Span ID；parent 为 nil 时用 context.Background()。
// 带截止时间的上下文占着定时器与 parent 上的一个登记，到截止时间或 parent 取消为止（与不调 cancel 的 context.WithDeadline 相同）；
// 调用一结束就该归还的（插件运行时的每次调用、长寿命的 parent）用 NewWithCancel
func New(parent context.Context, options ...Option) *Context {
	c, _ := create(parent, options)
	return c
}

// NewWithCancel 同 New，另返回释放函数，用法同 context.WithCancel（调用结束时 defer 它）：释放时上下文以取消结束
// （Err() 为 context.Canceled），parent 上的登记与截止时间的定时器立即归还，截止令牌此后不再置位；已经置位的保持。
// 没有截止时间的上下文同样可以释放。释放函数可以重复调用、可以并发调用
func NewWithCancel(parent context.Context, options ...Option) (*Context, context.CancelFunc) {
	c, release := create(parent, options)
	if release == nil {
		// 没有截止时间：只包一层取消，让释放的语义与带截止时间的相同
		ctx, cancel := context.WithCancel(c.Context)
		c.Context, release = ctx, cancel
	}
	return c, release
}

// create 按选项组装上下文；带截止时间时返回归还定时器与登记的函数，否则为 nil
func create(parent context.Context, options []Option) (*Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	c := &Context{Context: parent, baggage: map[string]string{}, items: &items{values: map[any]any{}}}
	for _, option := range options {
		option(c)
	}
	if c.traceID == "" {
		c.traceID = NewTraceID()
	}
	if c.spanID == "" {
		c.spanID = NewSpanID()
	}
	if c.deadline.IsZero() {
		return c, nil
	}
	// 截止时间与取消并存：到达截止时间时取消令牌触发（RFC-0003 规则 1）
	ctx, cancel := context.WithDeadline(parent, c.deadline)
	expiry := &deadlineToken{}
	// 截止令牌用自己的定时器：调用方先取消时 WithDeadline 的定时器随之停止，令牌照样要在截止时间置位。不经 context.AfterFunc(parent)
	// 等 parent 取消后再起定时器：那个登记要到 parent 取消才注销，长寿命的 parent 上每建一个上下文就多留一个
	timer := time.AfterFunc(time.Until(c.deadline), func() { expiry.fired.Store(true) })
	c.Context, c.expiry = ctx, expiry
	return c, func() {
		timer.Stop()
		cancel()
	}
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

// TraceFlags 返回上游给的采样位（0x00 或 0x01）；没有上游（链路的根）时第二个返回值为假（RFC-0003 规则 7）
func (c *Context) TraceFlags() (byte, bool) { return c.traceFlags, c.hasTraceFlags }

// TraceParent 返回 W3C traceparent：版本 00，标志只写采样位（00 或 01），上游的「不采样」原样传下去；没有上游时 SDK 没有采样器，写 01
func (c *Context) TraceParent() string {
	flags := SampledFlag
	if c.hasTraceFlags {
		flags = c.traceFlags
	}
	return "00-" + c.traceID + "-" + c.spanID + "-" + hex.EncodeToString([]byte{flags})
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

// WithBaggage 派生一个多一个 Baggage 键值的上下文；键必须符合 xihan.<name>。这是程序内逐项派生：给已登记的键（幂等键除外）
// 一个不能进入上下文的值（空、超过 256 个 UTF-16 码元、含控制字符或孤立的代理项即不是合法的 UTF-8）是编程错误，与键不合文法
// 一样 panic FrameworkError（PRIMITIVES_VALIDATION_001），不静默丢弃也不保留原值（conventions/baggage-keys.md，ADR-0235、ADR-0321）。
// 外部输入用选项 WithBaggage，那里丢弃不合的成员
func (c *Context) WithBaggage(key, value string) *Context {
	mustBaggageKey(key)
	if !primitives.IsBaggageEntryAcceptable(key, value) {
		panic(primitives.NewFrameworkError(primitives.ErrorCodeInvalidArgument, "The value of the baggage key '%s' must be non-empty, at most %d UTF-16 code units long, free of control characters and valid UTF-8 without lone surrogates.", key, primitives.MaxBaggageValueLength))
	}
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

// WithContext 派生一个换了底层 context.Context 的上下文（用法同 http.Request.WithContext）：取消与 Value 改看 ctx，追踪、Baggage、
// 对象袋与截止令牌同原来的上下文共享，原来的上下文不变。ctx 通常由本上下文派生（context.WithValue、WithCancel、WithTimeout），
// 这样调用的取消照样传得到它。截止时间取两者中较早的：ctx 自己的截止时间比这次调用的更早（或调用没有截止时间）时就是它，
// 到了按超时归因；更晚的不放宽调用的截止时间。ctx 为 nil 是编程错误，panic FrameworkError（PRIMITIVES_VALIDATION_001）
func (c *Context) WithContext(ctx context.Context) *Context {
	if ctx == nil {
		panic(primitives.NewFrameworkError(primitives.ErrorCodeInvalidArgument, "The context must not be nil."))
	}
	derived := *c
	derived.Context = ctx
	// Deadline、RemainingTime 等读的是 deadline 字段：ctx 更早的截止时间记进来，它们才与 Done 关闭的时刻一致
	if deadline, ok := ctx.Deadline(); ok && (c.deadline.IsZero() || deadline.Before(c.deadline)) {
		derived.deadline = deadline.UTC()
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

// Cancellation 把取消与超时翻译成原语层错误；未取消为 nil。归因只看截止令牌（RFC-0004、ADR-0092）：截止令牌已触发即
// KERNEL_TIMEOUT_001，即使调用方也取消了、而且取消在先；截止令牌没有触发时是 KERNEL_CANCELLED_001。不按谁先发生判断，
// 也不读时钟。嵌入的 Err() 仍是 context.Context 的原始错误（先触发者胜）
func (c *Context) Cancellation() *primitives.Error {
	err := c.Context.Err()
	if err == nil {
		return nil
	}
	if c.DeadlineExceeded() {
		return primitives.Timeout(ErrorCodeDeadlineExceeded, MessageDeadlineExceeded, nil)
	}
	return FromContextError(err)
}

// DeadlineExceeded 判断截止令牌是否已触发：截止时间已到（由定时器而不是时钟判断），不论调用方是否也取消了；
// 没有自己的截止时间时看嵌入的 context.Context 是否以 DeadlineExceeded 结束
func (c *Context) DeadlineExceeded() bool {
	if c.expiry != nil && c.expiry.fired.Load() {
		return true
	}
	return errors.Is(c.Context.Err(), context.DeadlineExceeded)
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

// SampledFlag 是 W3C trace-flags 的采样位
const SampledFlag byte = 0x01

// IsSampled 判断 trace-flags 是否采样：只看最低位（W3C trace-flags 是位域，03 也是采样）
func IsSampled(flags byte) bool { return flags&SampledFlag != 0 }

// ParseTraceParent 解析 W3C traceparent，返回追踪 ID、Span ID 与是否采样（只看标志的最低位）
func ParseTraceParent(traceParent string) (traceID, spanID string, sampled, ok bool) {
	traceID, spanID, flags, ok := parseTraceParent(traceParent)
	return traceID, spanID, ok && IsSampled(flags), ok
}

// parseTraceParent 解析 W3C traceparent，返回追踪 ID、Span ID 与原样的 trace-flags
func parseTraceParent(traceParent string) (traceID, spanID string, flags byte, ok bool) {
	match := traceParentPattern.FindStringSubmatch(traceParent)
	if match == nil || match[1] == "00000000000000000000000000000000" || match[2] == "0000000000000000" {
		return "", "", 0, false
	}
	decoded, err := hex.DecodeString(match[3])
	if err != nil {
		return "", "", 0, false
	}
	return match[1], match[2], decoded[0], true
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
		panic(primitives.NewFrameworkError(primitives.ErrorCodeInvalidArgument, "The baggage key '%s' must match xihan.<name>.", key))
	}
}
