package plugin

import (
	"reflect"
	"slices"
	"sync"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// lifecycle 是插件的状态机与健康快照：迁移按原语层的合法迁移表校验，状态、错误或健康项变化时通知健康订阅者。
type lifecycle struct {
	mu    sync.RWMutex
	state primitives.LifecycleState
	err   *primitives.Error
	// items 是最近一次健康回调报的健康项（已去掉未声明的）；只在可服务期间有
	items []HealthItem
	// claimed 记下已经开始过的一次性 RPC（Handshake、Configure、Start、Stop）
	claimed  map[string]bool
	watchers map[chan struct{}]struct{}
}

// newLifecycle 创建处于 Declared 的状态机。
func newLifecycle() *lifecycle {
	return &lifecycle{state: primitives.Declared, claimed: map[string]bool{}, watchers: map[chan struct{}]struct{}{}}
}

// current 返回当前状态与失败原因。
func (l *lifecycle) current() (primitives.LifecycleState, *primitives.Error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state, l.err
}

// is 判断是否处于给定状态之一。
func (l *lifecycle) is(states ...primitives.LifecycleState) bool {
	current, _ := l.current()
	return slices.Contains(states, current)
}

// claim 按 lifecycle.md §7.1 的次序表认领一次性的 RPC：当前状态在 accepted 里、这一步没有开始过时记下并返回真。
// 状态与认领在同一把锁里判定，并发的两次 Configure 只有一次能进去；配置回调执行期间状态仍是 Resolved，只看状态拦不住第二次
func (l *lifecycle) claim(step string, accepted ...primitives.LifecycleState) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.claimed[step] || !slices.Contains(accepted, l.state) {
		return false
	}
	l.claimed[step] = true
	return true
}

// transition 尝试迁移到新状态并记下失败原因；非法迁移返回假、什么都不改。离开可服务的状态时清掉健康项。
func (l *lifecycle) transition(to primitives.LifecycleState, err *primitives.Error) bool {
	return l.transitionFrom(to, err)
}

// transitionFrom 同 transition，另要求当前状态是 from 之一（from 为空时不限）；判定与迁移在同一把锁里。
func (l *lifecycle) transitionFrom(to primitives.LifecycleState, err *primitives.Error, from ...primitives.LifecycleState) bool {
	l.mu.Lock()
	if !l.state.CanTransitionTo(to) || (len(from) > 0 && !slices.Contains(from, l.state)) {
		l.mu.Unlock()
		return false
	}
	l.state = to
	l.err = err
	if !to.IsServing() {
		l.items = nil
	}
	watchers := l.watchersLocked()
	l.mu.Unlock()
	notify(watchers)
	return true
}

// report 记下健康回调这一次的结果并返回之后的快照（lifecycle.md §7.3、§7.4）：只在可服务期间（starting 时也在 Started）生效，
// 报 Ready、Degraded、Failed 的按迁移表照办，报其他状态的不改变状态；回调执行期间插件已经离开这些状态（如进入 Draining）时整个结果作废
func (l *lifecycle) report(reported Health, starting bool) Health {
	l.mu.Lock()
	before := l.snapshotLocked()
	if l.state.IsServing() || (starting && l.state == primitives.Started) {
		switch reported.State {
		case primitives.Ready, primitives.Degraded, primitives.Failed:
			// 同一状态不是迁移，只更新错误与健康项；Failed 之后合法的迁移只有回到 Resolved，健康回调报 Ready 也回不来
			if reported.State != l.state && l.state.CanTransitionTo(reported.State) {
				l.state = reported.State
			}
		}
		l.err = reported.Error
		// 没有健康项一律存成 nil：空切片与 nil 在比较快照时不相等，会让 WatchHealth 推出内容相同的一条
		l.items = nil
		if l.state.IsServing() && len(reported.Items) > 0 {
			l.items = slices.Clone(reported.Items)
		}
	}
	after := l.snapshotLocked()
	var watchers []chan struct{}
	if !reflect.DeepEqual(before, after) {
		watchers = l.watchersLocked()
	}
	l.mu.Unlock()
	notify(watchers)
	return after
}

// snapshot 返回当前的健康：状态、失败原因与可服务期间最近一次的健康项。
func (l *lifecycle) snapshot() Health {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.snapshotLocked()
}

// snapshotLocked 在持锁时组装快照；健康项复制一份，调用方改它不影响状态机。
func (l *lifecycle) snapshotLocked() Health {
	return Health{State: l.state, Items: slices.Clone(l.items), Error: l.err}
}

// watchersLocked 在持锁时取出订阅者。
func (l *lifecycle) watchersLocked() []chan struct{} {
	watchers := make([]chan struct{}, 0, len(l.watchers))
	for watcher := range l.watchers {
		watchers = append(watchers, watcher)
	}
	return watchers
}

// notify 通知订阅者。
func notify(watchers []chan struct{}) {
	// 通知在锁外做：订阅者的通道有缓冲，满了说明它还没处理上一次变化，跳过即可
	for _, watcher := range watchers {
		select {
		case watcher <- struct{}{}:
		default:
		}
	}
}

// watch 登记一个变化通知通道；返回注销函数。
func (l *lifecycle) watch() (<-chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	l.mu.Lock()
	l.watchers[channel] = struct{}{}
	l.mu.Unlock()
	return channel, func() {
		l.mu.Lock()
		delete(l.watchers, channel)
		l.mu.Unlock()
	}
}
