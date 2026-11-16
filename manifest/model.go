package manifest

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// ErrorCodeInvalidManifest 是读取清单失败的错误码（与 .NET KernelErrors.InvalidManifest 一致）
const ErrorCodeInvalidManifest = "KERNEL_CAPABILITY_006"

// 读取清单失败与跨字段约束的消息键，与 .NET 的 KernelMessages、ManifestMessages 一致
const (
	// MessageInvalidJSON：不是 JSON 或有重复成员，参数 {reason}，reason 是 $ 或第一处重复的路径
	MessageInvalidJSON = "kernel.manifest.invalid_json"
	// MessageEmpty：根是字面量 null，没有参数
	MessageEmpty = "kernel.manifest.empty"
	// MessageInvalid：不合 Schema，参数 {reason}，reason 是第一处问题的路径
	MessageInvalid = "kernel.manifest.invalid"
	// MessageFileMissing：清单文件不存在，参数 {path}
	MessageFileMissing = "kernel.manifest.file_missing"
	// MessageIsolationRequired：插件没有 isolation，参数 {id}
	MessageIsolationRequired = "primitives.manifest.isolation_required"
	// MessageIsolationNotAllowed：非插件写了 isolation 或 permissions，参数 {id}
	MessageIsolationNotAllowed = "primitives.manifest.isolation_not_allowed"
	// MessageDuplicateProvides：provides 的能力 ID 重复，参数 {id}
	MessageDuplicateProvides = "primitives.manifest.duplicate_provides"
	// MessageDuplicateRequires：requires 的能力 ID 重复，参数 {id}
	MessageDuplicateRequires = "primitives.manifest.duplicate_requires"
	// MessageDuplicateBindings：bindings 的名字重复，参数 {id}
	MessageDuplicateBindings = "primitives.manifest.duplicate_bindings"
)

// 这些键的 en-US 默认模板，逐字照 .NET 的 Kernel 与 Primitives 语言文件
func init() {
	primitives.RegisterDefaultTemplates(map[string]string{
		MessageInvalidJSON:         "The manifest is not valid JSON: {reason}",
		MessageEmpty:               "The manifest is empty.",
		MessageInvalid:             "The manifest is not valid: {reason}",
		MessageFileMissing:         "Manifest file '{path}' does not exist.",
		MessageIsolationRequired:   "Plugin manifest '{id}' must declare 'isolation'.",
		MessageIsolationNotAllowed: "Manifest '{id}' is not a plugin and must not declare 'isolation' or 'permissions'.",
		MessageDuplicateProvides:   "Manifest '{id}' declares the same capability ID more than once in 'provides'.",
		MessageDuplicateRequires:   "Manifest '{id}' declares the same capability ID more than once in 'requires'.",
		MessageDuplicateBindings:   "Manifest '{id}' declares the same binding name more than once in 'bindings'.",
	})
}

// SchemaURI 是写出的 $schema
const SchemaURI = "https://spec.xihanfun.com/manifest/v1.json"

// InterfaceVersion 是整数接口版本；线上编码为数字字符串（"1"）。
type InterfaceVersion int

// MarshalText 编码为数字字符串。
func (v InterfaceVersion) MarshalText() ([]byte, error) {
	if v < 1 {
		return nil, invalid("the interface version must be a positive integer, got %d", int(v))
	}
	return []byte(strconv.Itoa(int(v))), nil
}

// UnmarshalText 从数字字符串解码：不带前导零的正整数、不超过 2147483647（ADR-0236）；不接受 ^，只有 requires[].version 可以带，
// 由 CapabilityRequirement 自己解码
func (v *InterfaceVersion) UnmarshalText(text []byte) error {
	if !isPositiveInt32(string(text)) {
		return invalid("the interface version '%s' must be a positive integer up to 2147483647 written as a string of digits, without ^", text)
	}
	value, _ := strconv.Atoi(string(text))
	*v = InterfaceVersion(value)
	return nil
}

// requirementVersion 是 requires[].version：数字字符串，可以带一个 ^，^N 与 N 等价
type requirementVersion InterfaceVersion

// UnmarshalText 从 [^]N 解码
func (v *requirementVersion) UnmarshalText(text []byte) error {
	digits := strings.TrimPrefix(string(text), "^")
	if !isPositiveInt32(digits) {
		return invalid("the required interface version '%s' must be a positive integer up to 2147483647 written as a string of digits, optionally prefixed with ^", text)
	}
	value, _ := strconv.Atoi(digits)
	*v = requirementVersion(value)
	return nil
}

// CapabilityDeclaration 是提供的能力：id@version。
type CapabilityDeclaration struct {
	ID      string           `json:"id"`
	Version InterfaceVersion `json:"version"`
}

// ParseDeclaration 解析 id@version；版本不带前导零、不超过 2147483647，整串匹配
func ParseDeclaration(text string) (CapabilityDeclaration, bool) {
	match := declarationPattern.FindStringSubmatch(text)
	if match == nil || !isPositiveInt32(match[2]) {
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

// UnmarshalJSON 解码依赖：version 可以带 ^（Schema 里只有 requires[].version 可以）
func (r *CapabilityRequirement) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID       string             `json:"id"`
		Version  requirementVersion `json:"version"`
		Optional bool               `json:"optional"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*r = CapabilityRequirement{ID: wire.ID, Version: InterfaceVersion(wire.Version), Optional: wire.Optional}
	return nil
}

// ParseRequirement 解析 id@[^]version[?]；^N 与 N 等价；版本不带前导零、符号与空白，不超过 2147483647，整串匹配（ADR-0236）
func ParseRequirement(text string) (CapabilityRequirement, bool) {
	match := requirementPattern.FindStringSubmatch(text)
	if match == nil || !isPositiveInt32(match[3]) {
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

// ConfigContribution 是贡献的配置节：领域节 XiHan:<领域>，可再带一层或多层 PascalCase 子节（如 XiHan:Caching:Redis，RFC-0002「配置节」）。
// Schema 是可选的配置 Schema 文本：nil 表示没写，指向空串表示写了空串（写出时照写，与 .NET 一致）
type ConfigContribution struct {
	Section string  `json:"section"`
	Schema  *string `json:"schema,omitempty"`
}

// Contributions 是贡献点。Permissions 是此包定义的权限码（RFC-0008 权限码文法：小写、点分、至少两段，如 orders.read），
// 不重复；插件的由宿主在登记它的代理操作时一并登记（plugin/invocation.md §1）。
type Contributions struct {
	Pipeline    []PipelineContribution `json:"pipeline,omitempty"`
	Operations  []string               `json:"operations,omitempty"`
	Config      *ConfigContribution    `json:"config,omitempty"`
	Permissions []string               `json:"permissions,omitempty"`
	Health      []string               `json:"health,omitempty"`
}

// Manifest 是一份清单；字段顺序与 Schema 一致。可选的字符串 Package 与 Entry 用指针：nil 表示没写，指向空串表示写了空串，
// Write 照样写出 "package": ""（RFC-0002「写出」，与 .NET 一致；用 Text 取指针）
type Manifest struct {
	Schema      string                  `json:"$schema,omitempty"`
	ID          string                  `json:"id"`
	Package     *string                 `json:"package,omitempty"`
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
	Entry       *string                 `json:"entry,omitempty"`
}

// Read 读取清单 JSON（RFC-0002 解析规则 2，ADR-0236），按下面的顺序逐项执行、每一步只报第一处问题；失败都是 *primitives.Error，
// 错误码 KERNEL_CAPABILITY_006：
//  1. 共同的 JSON 读取检查（RFC-0001「JSON 文本」）：不是 JSON（含开头的 BOM，Read 不跳过它）为 kernel.manifest.invalid_json，
//     reason 是 $；任何一层有重复成员时同一个键，reason 是第一处重复的路径
//  2. 根是字面量 null：kernel.manifest.empty，没有参数
//  3. 按 Schema 逐项核对：kernel.manifest.invalid，reason 是第一处问题的路径（成员按文本顺序，各自依次查 Schema 之外的成员、null、
//     类型、取值，对象查完后按 required 的顺序查缺少的成员）
//  4. 跨字段约束（Validate）：保留 primitives.manifest.* 的键与 {id} 参数
func Read(data []byte) (*Manifest, error) {
	root, problem := jsontext.Parse(data)
	if problem != nil {
		return nil, primitives.NewError(ErrorCodeInvalidManifest, MessageInvalidJSON, map[string]any{"reason": problem.Path}, nil)
	}
	if root.Kind == jsontext.Null {
		return nil, primitives.NewError(ErrorCodeInvalidManifest, MessageEmpty, nil, nil)
	}
	if path, found := findShapeProblem(root); found {
		return nil, primitives.NewError(ErrorCodeInvalidManifest, MessageInvalid, map[string]any{"reason": path}, nil)
	}
	manifest := convert(root)
	if err := manifest.constraints(); err != nil {
		return nil, err.WithCode(ErrorCodeInvalidManifest)
	}
	return manifest, nil
}

// ReadFile 读取清单文件：按 UTF-8 读、跳过开头的 UTF-8 BOM，再照 Read；文件不存在（或是目录）为 KERNEL_CAPABILITY_006 +
// kernel.manifest.file_missing，参数 {path}。其他读取失败是 FrameworkError
func ReadFile(path string) (*Manifest, error) {
	info, err := os.Stat(path)
	if (err == nil && info.IsDir()) || errors.Is(err, fs.ErrNotExist) {
		return nil, primitives.NewError(ErrorCodeInvalidManifest, MessageFileMissing, map[string]any{"path": path}, nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &primitives.FrameworkError{Code: primitives.ErrorCodeInvalidArgument, Message: "The manifest file could not be read.", Cause: err}
	}
	return Read(trimBOM(data))
}

// trimBOM 去掉开头的 UTF-8 BOM
func trimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// Validate 核对清单：先按 Schema 核对它写出的形态（Write 的文本），不合时为 PRIMITIVES_VALIDATION_001 + kernel.manifest.invalid，
// reason 是路径；再按跨字段约束，顺序是插件没有 isolation（primitives.manifest.isolation_required）→ 非插件写了 isolation 或
// permissions（isolation_not_allowed）→ provides 重复（duplicate_provides）→ requires 重复（duplicate_requires）→ bindings 的
// 名字重复（duplicate_bindings），参数都只有 {id}，码是 PRIMITIVES_VALIDATION_001（与 .NET Manifest.Validate 一致；Read 改为
// KERNEL_CAPABILITY_006）。返回的错误是 *primitives.Error
func (m *Manifest) Validate() error {
	root, problem := jsontext.Parse(write(m))
	if problem != nil {
		return primitives.Validation(primitives.ErrorCodeInvalidArgument, MessageInvalidJSON, map[string]any{"reason": problem.Path})
	}
	if path, found := findShapeProblem(root); found {
		return primitives.Validation(primitives.ErrorCodeInvalidArgument, MessageInvalid, map[string]any{"reason": path})
	}
	if err := m.constraints(); err != nil {
		return err
	}
	return nil
}

// constraints 核对跨字段约束（RFC-0002 解析规则 2 第 4 步）
func (m *Manifest) constraints() *primitives.Error {
	fail := func(key string) *primitives.Error {
		return primitives.Validation(primitives.ErrorCodeInvalidArgument, key, map[string]any{"id": m.ID})
	}
	if m.Kind == KindPlugin && m.Isolation == "" {
		return fail(MessageIsolationRequired)
	}
	if m.Kind != KindPlugin && (m.Isolation != "" || len(m.Permissions) > 0) {
		return fail(MessageIsolationNotAllowed)
	}
	seen := map[string]bool{}
	for _, declaration := range m.Provides {
		if seen[declaration.ID] {
			return fail(MessageDuplicateProvides)
		}
		seen[declaration.ID] = true
	}
	seen = map[string]bool{}
	for _, requirement := range m.Requires {
		if seen[requirement.ID] {
			return fail(MessageDuplicateRequires)
		}
		seen[requirement.ID] = true
	}
	seen = map[string]bool{}
	for _, binding := range m.Bindings {
		if seen[binding.Name] {
			return fail(MessageDuplicateBindings)
		}
		seen[binding.Name] = true
	}
	return nil
}

// convert 把已核对形状的 JSON 转成清单
func convert(root *jsontext.Value) *Manifest {
	manifest := &Manifest{}
	for _, member := range root.Members {
		value := member.Value
		switch member.Name {
		case "$schema":
			manifest.Schema = value.Text
		case "id":
			manifest.ID = value.Text
		case "package":
			manifest.Package = Text(value.Text)
		case "version":
			manifest.Version = value.Text
		case "primitives":
			manifest.Primitives = versionOf(value.Text)
		case "stability":
			manifest.Stability, _ = primitives.ParseStability(value.Text)
		case "kind":
			manifest.Kind = Kind(value.Text)
		case "role":
			manifest.Role = Role(value.Text)
		case "isolation":
			manifest.Isolation = Isolation(value.Text)
		case "provides":
			manifest.Provides = make([]CapabilityDeclaration, 0, len(value.Items))
			for _, item := range value.Items {
				id, _ := item.Find("id")
				version, _ := item.Find("version")
				manifest.Provides = append(manifest.Provides, CapabilityDeclaration{ID: id.Text, Version: versionOf(version.Text)})
			}
		case "requires":
			manifest.Requires = make([]CapabilityRequirement, 0, len(value.Items))
			for _, item := range value.Items {
				id, _ := item.Find("id")
				version, _ := item.Find("version")
				requirement := CapabilityRequirement{ID: id.Text, Version: versionOf(strings.TrimPrefix(version.Text, "^"))}
				if optional, ok := item.Find("optional"); ok {
					requirement.Optional = optional.Bool
				}
				manifest.Requires = append(manifest.Requires, requirement)
			}
		case "bindings":
			manifest.Bindings = make([]Binding, 0, len(value.Items))
			for _, item := range value.Items {
				name, _ := item.Find("name")
				kind, _ := item.Find("type")
				binding := Binding{Name: name.Text, Type: kind.Text}
				if required, ok := item.Find("required"); ok {
					flag := required.Bool
					binding.Required = &flag
				}
				manifest.Bindings = append(manifest.Bindings, binding)
			}
		case "contributes":
			manifest.Contributes = convertContributions(value)
		case "activation":
			manifest.Activation = texts(value)
		case "permissions":
			manifest.Permissions = texts(value)
		case "entry":
			manifest.Entry = Text(value.Text)
		}
	}
	return manifest
}

// convertContributions 转换 contributes
func convertContributions(value *jsontext.Value) *Contributions {
	contributions := &Contributions{}
	for _, member := range value.Members {
		switch member.Name {
		case "pipeline":
			contributions.Pipeline = make([]PipelineContribution, 0, len(member.Value.Items))
			for _, item := range member.Value.Items {
				contribution := PipelineContribution{}
				for _, field := range item.Members {
					switch field.Name {
					case "middleware":
						contribution.Middleware = field.Value.Text
					case "stage":
						contribution.Stage, _ = primitives.ParsePipelineStage(field.Value.Text)
					case "before":
						contribution.Before = texts(field.Value)
					case "after":
						contribution.After = texts(field.Value)
					}
				}
				contributions.Pipeline = append(contributions.Pipeline, contribution)
			}
		case "operations":
			contributions.Operations = texts(member.Value)
		case "config":
			config := &ConfigContribution{}
			if section, ok := member.Value.Find("section"); ok {
				config.Section = section.Text
			}
			if schema, ok := member.Value.Find("schema"); ok {
				config.Schema = Text(schema.Text)
			}
			contributions.Config = config
		case "permissions":
			contributions.Permissions = texts(member.Value)
		case "health":
			contributions.Health = texts(member.Value)
		}
	}
	return contributions
}

// texts 读字符串数组
func texts(value *jsontext.Value) []string {
	items := make([]string, 0, len(value.Items))
	for _, item := range value.Items {
		items = append(items, item.Text)
	}
	return items
}

// Text 返回指向 text 的指针，给清单里可选的字符串（Package、Entry、ConfigContribution.Schema）赋值用
func Text(text string) *string { return &text }

// versionOf 读已核对的接口版本文本
func versionOf(text string) InterfaceVersion {
	value, _ := strconv.Atoi(text)
	return InterfaceVersion(value)
}

// Write 按 RFC-0002「写出」的排版写出清单（ADR-0236，各语言逐字相同）：两个空格缩进、换行只用 LF、末尾不换行、成员名与值之间是 ": "，
// 空数组写 []，非空数组每项一行；成员顺序固定，可选成员（package、isolation、config、schema、entry）没有值时不写，其余总写，
// role 缺省写 provider，activation 缺省写 ["onStartup"]，$schema 固定；可选字符串写了空串时照写；接口版本写不带 ^ 的十进制，字符串只转义必须转义的字符。
// 与 .NET 的 ManifestReader.Write 一样不做校验；manifest 为 nil 时返回 FrameworkError
func Write(manifest *Manifest) ([]byte, error) {
	if manifest == nil {
		return nil, invalid("there is no manifest to write")
	}
	return write(manifest), nil
}
