package primitives

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// messageKeyPattern 是消息键文法 ^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$（RFC-0001）。
var messageKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)

// placeholderPattern 匹配模板里的 {name} / {0} / {name:format} 占位符。
var placeholderPattern = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*|[0-9]+)(?::([^{}]*))?\}`)

// IsMessageKey 判断消息键是否合法。
func IsMessageKey(key string) bool { return messageKeyPattern.MatchString(key) }

// MessageKeyDomain 返回消息键的领域段（第一个点之前）；不合法时为空。
func MessageKeyDomain(key string) string {
	if !IsMessageKey(key) {
		return ""
	}
	return key[:strings.IndexByte(key, '.')]
}

// Placeholders 列出模板里的占位符名（按出现顺序、去重）。
func Placeholders(template string) []string {
	var names []string
	seen := map[string]bool{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			names = append(names, match[1])
		}
	}
	return names
}

// FormatMessage 用参数填充模板：{name} 取命名参数，{0} 取位置参数（键 "0"、"1"…），
// 可带 :format（按 Go 的 fmt 动词解释，如 {count:%05d}；其他写法原样 Sprint）；缺参数的占位符原样保留。
func FormatMessage(template string, arguments map[string]any) string {
	return placeholderPattern.ReplaceAllStringFunc(template, func(placeholder string) string {
		match := placeholderPattern.FindStringSubmatch(placeholder)
		value, ok := arguments[match[1]]
		if !ok {
			return placeholder
		}
		if format := match[2]; format != "" && strings.HasPrefix(format, "%") {
			return fmt.Sprintf(format, value)
		}
		return fmt.Sprint(value)
	})
}

// Positional 把位置参数转成以 "0"、"1"… 为键的参数表。
func Positional(values ...any) map[string]any {
	arguments := make(map[string]any, len(values))
	for index, value := range values {
		arguments[strconv.Itoa(index)] = value
	}
	return arguments
}

// Describe 在没有文案时描述一个消息：键 (k=v, ...)，参数按键排序。
func Describe(key string, arguments map[string]any) string {
	if len(arguments) == 0 {
		return key
	}
	names := make([]string, 0, len(arguments))
	for name := range arguments {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+fmt.Sprint(arguments[name]))
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

// ResolveMessage 用进程默认解析器解析消息键；没有解析器或没有该键时返回假。
func ResolveMessage(key string, arguments map[string]any) (string, bool) {
	defaultMessagesMu.RLock()
	resolver, locale := defaultResolver, defaultLocale
	defaultMessagesMu.RUnlock()
	if resolver == nil {
		return "", false
	}
	return resolver.Resolve(key, locale, arguments)
}

// Catalog 是一份内存消息目录：区域 → 键 → 模板；实现 MessageResolver，区域没有该键时回退到 en-US。
type Catalog map[string]map[string]string

// Resolve 实现 MessageResolver。
func (c Catalog) Resolve(key, locale string, arguments map[string]any) (string, bool) {
	if templates, ok := c[locale]; ok {
		if template, ok := templates[key]; ok {
			return FormatMessage(template, arguments), true
		}
	}
	if locale != "en-US" {
		if template, ok := c["en-US"][key]; ok {
			return FormatMessage(template, arguments), true
		}
	}
	return "", false
}
