package plugin

import (
	"context"
	"sync"
	"time"
)

// calls 记下在途的调用与投递：停止时据此等它们结束、到时触发它们的取消信号（lifecycle.md §7.5、§7.7），
// 回调前据此核对租户（invocation.md §5）。条目随调用结束删除，数量不超过同时在途的 RPC。
type calls struct {
	mu   sync.Mutex
	next uint64
	// active 是在途调用的取消函数
	active map[uint64]context.CancelFunc
	// tenants 是在途调用的租户及其个数：同一租户可以同时有多个调用
	tenants map[string]int
	// closed 为真表示停止已经开始，不再登记新的调用
	closed bool
	// drained 在 closed 之后、在途调用全部结束时关闭
	drained chan struct{}
}

// newCalls 创建空的登记表。
func newCalls() *calls {
	return &calls{active: map[uint64]context.CancelFunc{}, tenants: map[string]int{}, drained: make(chan struct{})}
}

// enter 登记一次调用：cancel 触发它的取消信号，tenant 是它的租户（可空）。停止开始之后返回假；
// 返回的函数在调用结束时调用一次。
func (c *calls) enter(cancel context.CancelFunc, tenant string) (func(), bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false
	}
	id := c.next
	c.next++
	c.active[id] = cancel
	if tenant != "" {
		c.tenants[tenant]++
	}
	var once sync.Once
	return func() { once.Do(func() { c.leave(id, tenant) }) }, true
}

// leave 注销一次调用。
func (c *calls) leave(id uint64, tenant string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.active, id)
	if tenant != "" {
		if c.tenants[tenant]--; c.tenants[tenant] <= 0 {
			delete(c.tenants, tenant)
		}
	}
	if c.closed && len(c.active) == 0 {
		c.markDrained()
	}
}

// close 不再登记新的调用；已经没有在途调用时立即算作结束。
func (c *calls) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if len(c.active) == 0 {
		c.markDrained()
	}
}

// markDrained 在持锁时关闭 drained，只关一次。
func (c *calls) markDrained() {
	select {
	case <-c.drained:
	default:
		close(c.drained)
	}
}

// cancelAll 触发全部在途调用的取消信号（RFC-0003：只经令牌通知，不强行中止处理器）。
func (c *calls) cancelAll() {
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.active))
	for _, cancel := range c.active {
		cancels = append(cancels, cancel)
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// wait 在 close 之后等在途调用全部结束，最长 timeout；到时仍有在途调用返回假。
func (c *calls) wait(timeout time.Duration) bool {
	// 已经结束时直接返回：下面的 select 在两个通道都就绪时随机选一个，零时长的定时器可能抢在前面
	select {
	case <-c.drained:
		return true
	default:
	}
	timer := time.NewTimer(max(timeout, 0))
	defer timer.Stop()
	select {
	case <-c.drained:
		return true
	case <-timer.C:
		return false
	}
}

// hasTenant 判断某个在途调用的租户是不是 tenant。
func (c *calls) hasTenant(tenant string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tenants[tenant] > 0
}
