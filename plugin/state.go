package plugin

import (
	"sync"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// lifecycle 是插件的状态机：迁移按原语层的合法迁移表校验，变化时通知健康订阅者。
type lifecycle struct {
	mu       sync.RWMutex
	state    primitives.LifecycleState
	err      *primitives.Error
	watchers map[chan struct{}]struct{}
}

// newLifecycle 创建处于 Declared 的状态机。
func newLifecycle() *lifecycle {
	return &lifecycle{state: primitives.Declared, watchers: map[chan struct{}]struct{}{}}
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
	for _, state := range states {
		if current == state {
			return true
		}
	}
	return false
}

// transition 尝试迁移到新状态；非法迁移返回假。
func (l *lifecycle) transition(to primitives.LifecycleState, err *primitives.Error) bool {
	l.mu.Lock()
	if !l.state.CanTransitionTo(to) {
		l.mu.Unlock()
		return false
	}
	l.state = to
	l.err = err
	watchers := make([]chan struct{}, 0, len(l.watchers))
	for watcher := range l.watchers {
		watchers = append(watchers, watcher)
	}
	l.mu.Unlock()
	// 通知在锁外做：订阅者的通道有缓冲，满了说明它还没处理上一次变化，跳过即可
	for _, watcher := range watchers {
		select {
		case watcher <- struct{}{}:
		default:
		}
	}
	return true
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
