package primitives

import "fmt"

// Stability 是五级稳定性（RFC-0001）。
type Stability int

// 稳定性等级。
const (
	Stable Stability = iota
	Preview
	Experimental
	Internal
	Deprecated
)

// stabilityNames 是清单与协议里的小写名。
var stabilityNames = [...]string{"stable", "preview", "experimental", "internal", "deprecated"}

// ParseStability 按小写名解析。
func ParseStability(name string) (Stability, bool) {
	for index, candidate := range stabilityNames {
		if candidate == name {
			return Stability(index), true
		}
	}
	return 0, false
}

// String 返回小写名。
func (s Stability) String() string {
	if !s.IsValid() {
		return fmt.Sprintf("Stability(%d)", int(s))
	}
	return stabilityNames[s]
}

// IsValid 判断数值是否对应一个等级。
func (s Stability) IsValid() bool { return s >= 0 && int(s) < len(stabilityNames) }

// MarshalText 按小写名编码。
func (s Stability) MarshalText() ([]byte, error) {
	if !s.IsValid() {
		return nil, &FrameworkError{Code: ErrorCodeInvalidArgument, Message: s.String() + " is not a stability."}
	}
	return []byte(s.String()), nil
}

// UnmarshalText 按小写名解码。
func (s *Stability) UnmarshalText(text []byte) error {
	stability, ok := ParseStability(string(text))
	if !ok {
		return &FrameworkError{Code: ErrorCodeInvalidArgument, Message: fmt.Sprintf("'%s' is not a stability.", text)}
	}
	*s = stability
	return nil
}
