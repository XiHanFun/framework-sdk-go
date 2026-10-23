package primitives

import "fmt"

// PipelineStage 是管道的十一个阶段（RFC-0004）；名称与顺序是规范性内容。
type PipelineStage int

// 管道阶段，按执行顺序。
const (
	Ingress PipelineStage = iota
	Observe
	Resilience
	Authenticate
	Tenant
	Authorize
	Validate
	Idempotency
	Transaction
	Handle
	Egress
)

// pipelineStageNames 是清单里的阶段名。
var pipelineStageNames = [...]string{"ingress", "observe", "resilience", "authenticate", "tenant", "authorize", "validate", "idempotency", "transaction", "handle", "egress"}

// PipelineStages 按顺序列出全部阶段。
func PipelineStages() []PipelineStage {
	stages := make([]PipelineStage, len(pipelineStageNames))
	for index := range stages {
		stages[index] = PipelineStage(index)
	}
	return stages
}

// ParsePipelineStage 按清单名解析阶段。
func ParsePipelineStage(name string) (PipelineStage, bool) {
	for index, candidate := range pipelineStageNames {
		if candidate == name {
			return PipelineStage(index), true
		}
	}
	return 0, false
}

// ManifestName 返回清单里的阶段名。
func (s PipelineStage) ManifestName() string {
	if !s.IsValid() {
		return fmt.Sprintf("PipelineStage(%d)", int(s))
	}
	return pipelineStageNames[s]
}

// String 返回清单里的阶段名。
func (s PipelineStage) String() string { return s.ManifestName() }

// IsValid 判断数值是否对应一个阶段。
func (s PipelineStage) IsValid() bool { return s >= 0 && int(s) < len(pipelineStageNames) }

// MarshalText 按清单名编码。
func (s PipelineStage) MarshalText() ([]byte, error) {
	if !s.IsValid() {
		return nil, &FrameworkError{Code: ErrorCodeInvalidArgument, Message: s.String() + " is not a pipeline stage."}
	}
	return []byte(s.ManifestName()), nil
}

// UnmarshalText 按清单名解码。
func (s *PipelineStage) UnmarshalText(text []byte) error {
	stage, ok := ParsePipelineStage(string(text))
	if !ok {
		return &FrameworkError{Code: ErrorCodeInvalidArgument, Message: fmt.Sprintf("'%s' is not a pipeline stage.", text)}
	}
	*s = stage
	return nil
}
