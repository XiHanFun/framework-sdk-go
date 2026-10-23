// Package manifest 是 xihan.manifest.json 的模型（RFC-0002，Schema 见 spec/manifest/manifest.schema.json）：
// 只做数据与格式校验；收集、解析与求解是宿主的职责。
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
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
	rangePattern        = regexp.MustCompile(`^(\^)?([1-9][0-9]*)$`)
	requirementPattern  = regexp.MustCompile(`^([a-z][a-z0-9]*\.[a-z][a-z0-9-]*)@(\^?)([1-9][0-9]*)(\??)$`)
	declarationPattern  = regexp.MustCompile(`^([a-z][a-z0-9]*\.[a-z][a-z0-9-]*)@([1-9][0-9]*)$`)
	activationPattern   = regexp.MustCompile(`^(onStartup|onOperation:[a-z0-9.*-]+|onEvent:[a-z0-9.*-]+)$`)
	permissionPattern   = regexp.MustCompile(`^(net\.outbound|fs\.read|fs\.write|capability):.+$`)
	bindingNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	bindingTypePattern  = regexp.MustCompile(`^[a-z]+(:[a-z]+)?$`)
	operationPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z][a-z0-9-]*$`)
	sectionPattern      = regexp.MustCompile(`^XiHan:[A-Z][A-Za-z0-9]*$`)
	healthPattern       = regexp.MustCompile(`^[a-z][a-z0-9.-]*$`)
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

// InterfaceVersion 是整数接口版本；线上编码为数字字符串（"1"）。
type InterfaceVersion int

// MarshalText 编码为数字字符串。
func (v InterfaceVersion) MarshalText() ([]byte, error) {
	if v < 1 {
		return nil, invalid("the interface version must be a positive integer, got %d", int(v))
	}
	return []byte(strconv.Itoa(int(v))), nil
}

// UnmarshalText 从数字字符串解码；^N 与 N 等价。
func (v *InterfaceVersion) UnmarshalText(text []byte) error {
	match := rangePattern.FindSubmatch(text)
	if match == nil {
		return invalid("the interface version '%s' must be a positive integer, optionally prefixed with ^", text)
	}
	value, _ := strconv.Atoi(string(match[2]))
	*v = InterfaceVersion(value)
	return nil
}

// CapabilityDeclaration 是提供的能力：id@version。
type CapabilityDeclaration struct {
	ID      string           `json:"id"`
	Version InterfaceVersion `json:"version"`
}

// ParseDeclaration 解析 id@version。
func ParseDeclaration(text string) (CapabilityDeclaration, bool) {
	match := declarationPattern.FindStringSubmatch(text)
	if match == nil {
		return CapabilityDeclaration{}, false
	}
	version, _ := strconv.Atoi(match[2])
	return CapabilityDeclaration{ID: match[1], Version: InterfaceVersion(version)}, true
}

// String 返回 id@version。
func (d CapabilityDeclaration) String() string { return d.ID + "@" + strconv.Itoa(int(d.Version)) }

// CapabilityRequirement 是依赖的能力：id@[^]version[?]。
type CapabilityRequirement struct {
	ID       string           `json:"id"`
	Version  InterfaceVersion `json:"version"`
	Optional bool             `json:"optional,omitempty"`
}

// ParseRequirement 解析 id@[^]version[?]；^N 与 N 等价。
func ParseRequirement(text string) (CapabilityRequirement, bool) {
	match := requirementPattern.FindStringSubmatch(text)
	if match == nil {
		return CapabilityRequirement{}, false
	}
	version, _ := strconv.Atoi(match[3])
	return CapabilityRequirement{ID: match[1], Version: InterfaceVersion(version), Optional: match[4] == "?"}, true
}

// String 返回 id@version[?]。
func (r CapabilityRequirement) String() string {
	text := r.ID + "@" + strconv.Itoa(int(r.Version))
	if r.Optional {
		text += "?"
	}
	return text
}

// IsSatisfiedBy 判断一个声明是否满足本依赖：同一能力且同一接口版本。
func (r CapabilityRequirement) IsSatisfiedBy(declaration CapabilityDeclaration) bool {
	return r.ID == declaration.ID && r.Version == declaration.Version
}

// Binding 是需要宿主注入的资源。
type Binding struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required *bool  `json:"required,omitempty"`
}

// IsRequired 判断绑定是否必需；缺省为必需。
func (b Binding) IsRequired() bool { return b.Required == nil || *b.Required }

// PipelineContribution 是贡献的一个中间件。
type PipelineContribution struct {
	Middleware string                   `json:"middleware"`
	Stage      primitives.PipelineStage `json:"stage"`
	Before     []string                 `json:"before,omitempty"`
	After      []string                 `json:"after,omitempty"`
}

// ConfigContribution 是贡献的配置节。
type ConfigContribution struct {
	Section string `json:"section"`
	Schema  string `json:"schema,omitempty"`
}

// Contributions 是贡献点。
type Contributions struct {
	Pipeline    []PipelineContribution `json:"pipeline,omitempty"`
	Operations  []string               `json:"operations,omitempty"`
	Config      *ConfigContribution    `json:"config,omitempty"`
	Permissions []string               `json:"permissions,omitempty"`
	Health      []string               `json:"health,omitempty"`
}

// Manifest 是一份清单；字段顺序与 Schema 一致。
type Manifest struct {
	Schema      string                  `json:"$schema,omitempty"`
	ID          string                  `json:"id"`
	Package     string                  `json:"package,omitempty"`
	Version     string                  `json:"version"`
	Primitives  InterfaceVersion        `json:"primitives"`
	Stability   primitives.Stability    `json:"stability"`
	Kind        Kind                    `json:"kind"`
	Role        Role                    `json:"role,omitempty"`
	Isolation   Isolation               `json:"isolation,omitempty"`
	Provides    []CapabilityDeclaration `json:"provides,omitempty"`
	Requires    []CapabilityRequirement `json:"requires,omitempty"`
	Bindings    []Binding               `json:"bindings,omitempty"`
	Contributes *Contributions          `json:"contributes,omitempty"`
	Activation  []string                `json:"activation,omitempty"`
	Permissions []string                `json:"permissions,omitempty"`
	Entry       string                  `json:"entry,omitempty"`
}

// Read 解析并校验清单 JSON；未知字段、格式与跨字段约束都算失败（PRIMITIVES_VALIDATION_001）。
func Read(data []byte) (*Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, &primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: "The manifest is not valid JSON for the schema.", Cause: err}
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// ReadFile 读取并校验清单文件。
func ReadFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: "The manifest file could not be read.", Cause: err}
	}
	return Read(data)
}

// Write 输出规范化的清单 JSON（两空格缩进，字段按 Schema 顺序，缺省值省略）。
func Write(manifest *Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(manifest, "", "  ")
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

// Validate 校验格式与跨字段约束：插件必须声明隔离，非插件不得声明隔离或权限，提供、依赖与绑定不得重复。
func (m *Manifest) Validate() error {
	if !IsManifestID(m.ID) {
		return invalid("the manifest ID '%s' must match xihan.<domain>.<name>", m.ID)
	}
	if !semverPattern.MatchString(m.Version) {
		return invalid("the version '%s' of '%s' must be a semantic version", m.Version, m.ID)
	}
	if m.Primitives < 1 {
		return invalid("the primitives version of '%s' must be a positive integer", m.ID)
	}
	if !m.Stability.IsValid() {
		return invalid("the stability of '%s' is not valid", m.ID)
	}
	switch m.Kind {
	case KindCapability, KindProvider, KindPlugin, KindHost, KindProtocol:
	default:
		return invalid("the kind '%s' of '%s' is not valid", m.Kind, m.ID)
	}
	switch m.Role {
	case "", RoleProvider, RoleDefault:
	default:
		return invalid("the role '%s' of '%s' is not valid", m.Role, m.ID)
	}
	switch m.Isolation {
	case "", IsolationInProcess, IsolationWasm, IsolationOutOfProcess:
	default:
		return invalid("the isolation '%s' of '%s' is not valid", m.Isolation, m.ID)
	}
	if m.Kind == KindPlugin && m.Isolation == "" {
		return invalid("the plugin '%s' must declare its isolation", m.ID)
	}
	if m.Kind != KindPlugin && m.Isolation != "" {
		return invalid("'%s' is not a plugin and must not declare an isolation", m.ID)
	}
	if m.Kind != KindPlugin && len(m.Permissions) > 0 {
		return invalid("'%s' is not a plugin and must not declare permissions", m.ID)
	}

	seen := map[string]bool{}
	for _, declaration := range m.Provides {
		if !IsCapabilityID(declaration.ID) || declaration.Version < 1 {
			return invalid("the provided capability '%s' of '%s' is not valid", declaration.ID, m.ID)
		}
		if seen[declaration.ID] {
			return invalid("'%s' provides '%s' more than once", m.ID, declaration.ID)
		}
		seen[declaration.ID] = true
	}
	seen = map[string]bool{}
	for _, requirement := range m.Requires {
		if !IsCapabilityID(requirement.ID) || requirement.Version < 1 {
			return invalid("the required capability '%s' of '%s' is not valid", requirement.ID, m.ID)
		}
		if seen[requirement.ID] {
			return invalid("'%s' requires '%s' more than once", m.ID, requirement.ID)
		}
		seen[requirement.ID] = true
	}
	seen = map[string]bool{}
	for _, binding := range m.Bindings {
		if !bindingNamePattern.MatchString(binding.Name) || !bindingTypePattern.MatchString(binding.Type) {
			return invalid("the binding '%s' (%s) of '%s' is not valid", binding.Name, binding.Type, m.ID)
		}
		if seen[binding.Name] {
			return invalid("'%s' declares the binding '%s' more than once", m.ID, binding.Name)
		}
		seen[binding.Name] = true
	}
	for _, event := range m.Activation {
		if !activationPattern.MatchString(event) {
			return invalid("the activation event '%s' of '%s' is not valid", event, m.ID)
		}
	}
	for _, permission := range m.Permissions {
		if !permissionPattern.MatchString(permission) {
			return invalid("the permission '%s' of '%s' is not valid", permission, m.ID)
		}
	}
	if m.Contributes != nil {
		if err := m.Contributes.validate(m.ID); err != nil {
			return err
		}
	}
	return nil
}

// validate 校验贡献点。
func (c *Contributions) validate(id string) error {
	for _, contribution := range c.Pipeline {
		if contribution.Middleware == "" {
			return invalid("a pipeline contribution of '%s' has no middleware name", id)
		}
		if !contribution.Stage.IsValid() {
			return invalid("the pipeline contribution '%s' of '%s' has an unknown stage", contribution.Middleware, id)
		}
	}
	for _, operation := range c.Operations {
		if !operationPattern.MatchString(operation) {
			return invalid("the operation '%s' of '%s' must match <resource>.<verb>", operation, id)
		}
	}
	if c.Config != nil && !sectionPattern.MatchString(c.Config.Section) {
		return invalid("the configuration section '%s' of '%s' must match XiHan:<Domain>", c.Config.Section, id)
	}
	for _, health := range c.Health {
		if !healthPattern.MatchString(health) {
			return invalid("the health item '%s' of '%s' is not valid", health, id)
		}
	}
	return nil
}

// invalid 创建清单校验失败的配置错误。
func invalid(format string, arguments ...any) error {
	return &primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: fmt.Sprintf(format, arguments...) + "."}
}
