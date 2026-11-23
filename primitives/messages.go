package primitives

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
)

// MaxMessageKeyLength 是消息键的最大长度（RFC-0001、RFC-0010）
const MaxMessageKeyLength = 128

// messageKeyPattern 是消息键文法（RFC-0001、RFC-0010）：小写字母开头的点分段，至少两段，每段可含下划线；
// 首段是领域词或应用自定义的小写标识（操作 ID 的资源段把连字符换成下划线后即是，如 tenant_members.list.description）
var messageKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// IsMessageKey 判断消息键是否合法：符合文法且不超过 MaxMessageKeyLength 个字符
func IsMessageKey(key string) bool {
	return len(key) <= MaxMessageKeyLength && messageKeyPattern.MatchString(key)
}

// MessageKeyDomain 返回消息键的领域段（第一个点之前）；不合法时为空。
func MessageKeyDomain(key string) string {
	if !IsMessageKey(key) {
		return ""
	}
	return key[:strings.IndexByte(key, '.')]
}

// Positional 把位置参数转成以 "0"、"1"… 为键的参数表。
func Positional(values ...any) map[string]any {
	arguments := make(map[string]any, len(values))
	for index, value := range values {
		arguments[strconv.Itoa(index)] = value
	}
	return arguments
}

// Describe 在没有文案时描述一个消息（RFC-0010）：没有参数时只有键，有参数时写 key (name=value, …)；
// 值按 FormatValue 写（数组与对象是紧凑的 JSON），参数按名字的序数顺序（逐个 UTF-16 码元比较，与 .NET 的 StringComparer.Ordinal 相同，ADR-0235）排列
func Describe(key string, arguments map[string]any) string {
	if len(arguments) == 0 {
		return key
	}
	names := make([]string, 0, len(arguments))
	for name := range arguments {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return compareUTF16(names[i], names[j]) < 0 })
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+FormatValue(arguments[name]))
	}
	return key + " (" + strings.Join(parts, ", ") + ")"
}

// MessageResolver 按区域把消息键解析成已格式化的文案（RFC-0010）。
type MessageResolver interface {
	// Resolve 返回文案；没有该键或区域时返回假。
	Resolve(key, locale string, arguments map[string]any) (string, bool)
}

// MessageResolverFunc 让函数实现 MessageResolver。
type MessageResolverFunc func(key, locale string, arguments map[string]any) (string, bool)

// Resolve 实现 MessageResolver。
func (f MessageResolverFunc) Resolve(key, locale string, arguments map[string]any) (string, bool) {
	return f(key, locale, arguments)
}

// 进程默认的解析器与区域（RFC-0001 MessageScope.Default）。
var (
	defaultMessagesMu sync.RWMutex
	defaultResolver   MessageResolver
	defaultLocale     = "en-US"
)

// SetDefaultMessages 设置进程默认的消息解析器与区域；Error.Message 在没有已解析文案时用它。
func SetDefaultMessages(resolver MessageResolver, locale string) {
	defaultMessagesMu.Lock()
	defer defaultMessagesMu.Unlock()
	defaultResolver = resolver
	if locale != "" {
		defaultLocale = locale
	}
}

// DefaultLocale 返回进程默认区域。
func DefaultLocale() string {
	defaultMessagesMu.RLock()
	defer defaultMessagesMu.RUnlock()
	return defaultLocale
}

// ResolveMessage 解析消息键：先用进程默认解析器，解析不出时用 SDK 各包登记的 en-US 默认模板（严格格式化）；
// 都解析不出时返回假，调用方退回 Describe
func ResolveMessage(key string, arguments map[string]any) (string, bool) {
	defaultMessagesMu.RLock()
	resolver, locale := defaultResolver, defaultLocale
	defaultMessagesMu.RUnlock()
	if resolver != nil {
		if text, ok := resolver.Resolve(key, locale, arguments); ok {
			return text, true
		}
	}
	if template, ok := DefaultTemplate(key); ok {
		return TryFormatMessage(template, arguments)
	}
	return "", false
}

// MessageUninitializedResult 是未初始化结果（PRIMITIVES_VALIDATION_002）的消息键
const MessageUninitializedResult = "primitives.result.uninitialized"

// defaultTemplates 是 SDK 各包登记的 en-US 默认模板（RFC-0010：每个键都有 en-US 文案），逐字照 .NET 参考实现的语言文件
var defaultTemplates = struct {
	sync.RWMutex
	values map[string]string
}{values: map[string]string{
	MessageUninitializedResult: "The result is uninitialized: a default value is not a valid result.",
}}

// RegisterDefaultTemplates 登记一组 en-US 默认模板：进程默认解析器解析不出这些键时 Error.Message 用它们。
// 内核各包（invocation、operation）在初始化时登记自己的键；键不合法或与已登记的模板不同是编程错误，panic FrameworkError
func RegisterDefaultTemplates(templates map[string]string) {
	defaultTemplates.Lock()
	defer defaultTemplates.Unlock()
	for key, template := range templates {
		if !IsMessageKey(key) {
			panic(NewFrameworkError(ErrorCodeInvalidArgument, "The message key '%s' of a default template is not valid.", key))
		}
		if existing, ok := defaultTemplates.values[key]; ok && existing != template {
			panic(NewFrameworkError(ErrorCodeInvalidArgument, "The default template of '%s' is already registered with another text.", key))
		}
		defaultTemplates.values[key] = template
	}
}

// DefaultTemplate 返回消息键的 en-US 默认模板；没有登记时第二个返回值为假
func DefaultTemplate(key string) (string, bool) {
	defaultTemplates.RLock()
	defer defaultTemplates.RUnlock()
	template, ok := defaultTemplates.values[key]
	return template, ok
}

// Catalog 是一份内存消息目录：区域 → 键 → 模板；实现 MessageResolver，区域没有该键时回退到 en-US，区域名比较不区分大小写
type Catalog map[string]map[string]string

// Resolve 实现 MessageResolver（RFC-0010）：按严格格式化解析，找不到键或任一占位符缺值即返回假，调用方退回 Describe。
// 数字按命中模板的区域取小数点与分组符（与 .NET 的本地化器一样，回退到 en-US 的模板按 en-US 写）
func (c Catalog) Resolve(key, locale string, arguments map[string]any) (string, bool) {
	template, resolved, ok := c.template(locale, key)
	if !ok && !strings.EqualFold(locale, DefaultCatalogLocale) {
		template, resolved, ok = c.template(DefaultCatalogLocale, key)
	}
	if !ok {
		return "", false
	}
	return TryFormatMessageIn(resolved, template, arguments)
}

// template 按区域（不区分大小写）与键取模板，并返回命中的区域
func (c Catalog) template(locale, key string) (string, string, bool) {
	if templates, ok := c[locale]; ok {
		if template, ok := templates[key]; ok {
			return template, locale, true
		}
	}
	for candidate, templates := range c {
		if candidate != locale && strings.EqualFold(candidate, locale) {
			if template, ok := templates[key]; ok {
				return template, candidate, true
			}
		}
	}
	return "", "", false
}

// DefaultCatalogLocale 是消息目录的默认区域：目录里找不到请求区域的键时回退到它（RFC-0010）
const DefaultCatalogLocale = "en-US"

// compareUTF16 按 UTF-16 码元逐个比较两个字符串（序数比较）：与 Go 字符串的字节序只在基本多文种平面以外的字符上不同，
// 那里 UTF-16 的代理项（U+D800–U+DFFF）排在 U+E000–U+FFFF 之前
func compareUTF16(left, right string) int {
	a, b := utf16.Encode([]rune(left)), utf16.Encode([]rune(right))
	for index := 0; index < len(a) && index < len(b); index++ {
		if a[index] != b[index] {
			if a[index] < b[index] {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}
