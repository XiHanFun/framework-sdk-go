package primitives

import "fmt"

// LifecycleState 是九态生命周期（RFC-0001）；数值与 .NET 枚举和插件协议的 LifecycleState 一致。
type LifecycleState int

// 生命周期状态。
const (
	Declared LifecycleState = iota
	Resolved
	Configured
	Started
	Ready
	Degraded
	Draining
	Stopped
	Failed
)

// lifecycleStateNames 是状态名。
var lifecycleStateNames = [...]string{"Declared", "Resolved", "Configured", "Started", "Ready", "Degraded", "Draining", "Stopped", "Failed"}

// lifecycleTransitions 是合法迁移的完整列表（conformance/vectors/primitives/lifecycle-transitions.json）；未列出的一律非法。
var lifecycleTransitions = map[LifecycleState][]LifecycleState{
	Declared:   {Resolved, Failed},
	Resolved:   {Configured, Failed},
	Configured: {Started, Failed},
	Started:    {Ready, Degraded, Draining, Failed},
	Ready:      {Degraded, Draining, Failed},
	Degraded:   {Ready, Draining, Failed},
	Draining:   {Stopped, Failed},
	Stopped:    {},
	Failed:     {Resolved},
}

// LifecycleStates 按数值顺序列出全部状态。
func LifecycleStates() []LifecycleState {
	states := make([]LifecycleState, len(lifecycleStateNames))
	for index := range states {
		states[index] = LifecycleState(index)
	}
	return states
}

// ParseLifecycleState 按名字解析状态。
func ParseLifecycleState(name string) (LifecycleState, bool) {
	for index, candidate := range lifecycleStateNames {
		if candidate == name {
			return LifecycleState(index), true
		}
	}
	return 0, false
}

// String 返回状态名。
func (s LifecycleState) String() string {
	if s < 0 || int(s) >= len(lifecycleStateNames) {
		return fmt.Sprintf("LifecycleState(%d)", int(s))
	}
	return lifecycleStateNames[s]
}

// IsValid 判断数值是否对应一个状态。
func (s LifecycleState) IsValid() bool { return s >= 0 && int(s) < len(lifecycleStateNames) }

// CanTransitionTo 判断迁移是否合法。
func (s LifecycleState) CanTransitionTo(to LifecycleState) bool {
	for _, allowed := range lifecycleTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// IsServing 判断是否可服务：Ready 或 Degraded。
func (s LifecycleState) IsServing() bool { return s == Ready || s == Degraded }

// IsTerminal 判断是否终态：Stopped。
func (s LifecycleState) IsTerminal() bool { return s == Stopped }

// MarshalText 按名字编码。
func (s LifecycleState) MarshalText() ([]byte, error) {
	if !s.IsValid() {
		return nil, &FrameworkError{Code: ErrorCodeInvalidArgument, Message: s.String() + " is not a lifecycle state."}
	}
	return []byte(s.String()), nil
}

// UnmarshalText 按名字解码。
func (s *LifecycleState) UnmarshalText(text []byte) error {
	state, ok := ParseLifecycleState(string(text))
	if !ok {
		return &FrameworkError{Code: ErrorCodeInvalidArgument, Message: fmt.Sprintf("'%s' is not a lifecycle state.", text)}
	}
	*s = state
	return nil
}
