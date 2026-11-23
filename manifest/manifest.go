// Package manifest 是 xihan.manifest.json 的模型（RFC-0002，Schema 见 spec/manifest/manifest.schema.json）：读取、规范排版的写出与校验，
// 读取的检查顺序、错误与写出的排版与 .NET 的 ManifestReader 逐字一致（ADR-0236）；收集、解析与求解是宿主的职责。
package manifest

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// FileName 是清单文件名。
const FileName = "xihan.manifest.json"

// Kind 是清单种类。
type Kind string

// 清单种类。
const (
	KindCapability Kind = "capability"
	KindProvider   Kind = "provider"
	KindPlugin     Kind = "plugin"
	KindHost       Kind = "host"
	KindProtocol   Kind = "protocol"
)

// Role 是 Provider 角色。
type Role string

// Provider 角色：default 在没有显式 Provider 时被选中。
const (
	RoleProvider Role = "provider"
	RoleDefault  Role = "default"
)

// Isolation 是插件隔离级别。
type Isolation string

// 插件隔离级别。
const (
	IsolationInProcess    Isolation = "in-process"
	IsolationWasm         Isolation = "wasm"
	IsolationOutOfProcess Isolation = "out-of-process"
)

var (
	manifestIDPattern   = regexp.MustCompile(`^xihan\.[a-z][a-z0-9]*\.[a-z][a-z0-9-]*$`)
	capabilityIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9-]*$`)
	semverPattern       = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	versionPattern      = regexp.MustCompile(`^[1-9][0-9]*$`)
	requirementPattern  = regexp.MustCompile(`^([a-z][a-z0-9]*\.[a-z][a-z0-9-]*)@(\^?)([1-9][0-9]*)(\??)$`)
	declarationPattern  = regexp.MustCompile(`^([a-z][a-z0-9]*\.[a-z][a-z0-9-]*)@([1-9][0-9]*)$`)
	activationPattern   = regexp.MustCompile(`^(onStartup|onOperation:[a-z0-9.*-]+|onEvent:[a-z0-9.*-]+)$`)
	permissionPattern   = regexp.MustCompile(`^(net\.outbound|fs\.read|fs\.write|capability):.+$`)
	bindingNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	bindingTypePattern  = regexp.MustCompile(`^[a-z]+(:[a-z]+)?$`)
	sectionPattern      = regexp.MustCompile(`^XiHan:[A-Z][A-Za-z0-9]*(:[A-Z][A-Za-z0-9]*)*$`)
	healthPattern       = regexp.MustCompile(`^[a-z][a-z0-9.-]*$`)
	permCodePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`)
)

// IsManifestID 判断清单 ID 是否符合 xihan.<领域>.<名称>。
func IsManifestID(id string) bool { return manifestIDPattern.MatchString(id) }

// IsCapabilityID 判断能力 ID 是否符合 <领域>.<能力>。
func IsCapabilityID(id string) bool { return capabilityIDPattern.MatchString(id) }

// Domain 返回清单 ID 或能力 ID 的领域段；不合法时为空。
func Domain(id string) string {
	parts := strings.Split(id, ".")
	switch {
	case IsManifestID(id):
		return parts[1]
	case IsCapabilityID(id):
		return parts[0]
	default:
		return ""
	}
}

// EffectiveRole 返回角色，缺省为 provider。
func (m *Manifest) EffectiveRole() Role {
	if m.Role == "" {
		return RoleProvider
	}
	return m.Role
}

// EffectiveActivation 返回激活事件，缺省为 onStartup。
func (m *Manifest) EffectiveActivation() []string {
	if len(m.Activation) == 0 {
		return []string{"onStartup"}
	}
	return m.Activation
}

// ActivatesOnStartup 判断是否在宿主启动时激活。
func (m *Manifest) ActivatesOnStartup() bool {
	for _, event := range m.EffectiveActivation() {
		if event == "onStartup" {
			return true
		}
	}
	return false
}

// Operations 返回贡献的操作 ID；没有贡献时为空。
func (m *Manifest) Operations() []string {
	if m.Contributes == nil {
		return nil
	}
	return m.Contributes.Operations
}

// Health 返回贡献的健康项；没有贡献时为空。
func (m *Manifest) Health() []string {
	if m.Contributes == nil {
		return nil
	}
	return m.Contributes.Health
}

// ConfigSection 返回贡献的配置节；没有时为空。
func (m *Manifest) ConfigSection() string {
	if m.Contributes == nil || m.Contributes.Config == nil {
		return ""
	}
	return m.Contributes.Config.Section
}

// invalid 创建清单校验失败的配置错误。
func invalid(format string, arguments ...any) error {
	return primitives.NewFrameworkError(primitives.ErrorCodeInvalidArgument, "%s.", fmt.Sprintf(format, arguments...))
}
