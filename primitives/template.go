package primitives

import (
	"regexp"
	"strings"
)

// placeholderNamePattern 是占位符名文法（RFC-0010）：字母开头的字母、数字与下划线（按名字），或纯数字（按位置）
var placeholderNamePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*|[0-9]+)$`)

// segment 是模板里的一段：占位符（isPlaceholder 为真，name 与可选 format）或字面量
type segment struct {
	isPlaceholder bool
	literal       string
	name          string
	format        string
	hasFormat     bool
}

// readSegment 从 index 读取模板的下一段并返回下一段的起点（与 .NET MessageTemplate 逐字一致）：
// {{ 与 }} 是转义的单个花括号；没有闭合的 { 连同其后全部文本是字面量；花括号里不是合法占位符名的（如 JSON 片段）连同花括号是字面量
func readSegment(text string, index int) (segment, int) {
	c := text[index]
	if (c == '{' || c == '}') && index+1 < len(text) && text[index+1] == c {
		return segment{literal: text[index : index+1]}, index + 2
	}
	if c != '{' {
		end := index + 1
		for end < len(text) && text[end] != '{' && text[end] != '}' {
			end++
		}
		return segment{literal: text[index:end]}, end
	}
	closing := strings.IndexByte(text[index+1:], '}')
	if closing < 0 {
		return segment{literal: text[index:]}, len(text)
	}
	end := index + 1 + closing
	body := text[index+1 : end]
	name, format, hasFormat := body, "", false
	if separator := strings.IndexByte(body, ':'); separator >= 0 {
		name, format, hasFormat = body[:separator], body[separator+1:], true
	}
	if !placeholderNamePattern.MatchString(name) {
		return segment{literal: text[index : end+1]}, end + 1
	}
	return segment{isPlaceholder: true, name: name, format: format, hasFormat: hasFormat}, end + 1
}

// Placeholders 列出模板里的占位符名（不含格式说明符），按出现顺序去重；转义与字面量花括号不算
func Placeholders(template string) []string {
	var names []string
	seen := map[string]bool{}
	for index := 0; index < len(template); {
		var part segment
		part, index = readSegment(template, index)
		if part.isPlaceholder && !seen[part.name] {
			seen[part.name] = true
			names = append(names, part.name)
		}
	}
	return names
}

// FormatMessage 宽松格式化模板（RFC-0010）：{name} 取命名参数，{0} 取位置参数（键 "0"、"1"…），可带 :format；
// {{ 与 }} 是转义；缺值的占位符连同 :format 原样保留，让遗漏可见。按不变文化写数字
func FormatMessage(template string, arguments map[string]any) string {
	text, _ := render(template, arguments, "", false)
	return text
}

// TryFormatMessage 严格格式化模板：任一占位符缺值返回假，供消息目录拒绝半截译文、调用方退回 Describe。按不变文化写数字
func TryFormatMessage(template string, arguments map[string]any) (string, bool) {
	return render(template, arguments, "", true)
}

// FormatMessageIn 按区域宽松格式化模板：区域只改变小数点与分组符（见 FormatArgumentIn），其余同 FormatMessage
func FormatMessageIn(locale, template string, arguments map[string]any) string {
	text, _ := render(template, arguments, locale, false)
	return text
}

// TryFormatMessageIn 按区域严格格式化模板，其余同 TryFormatMessage
func TryFormatMessageIn(locale, template string, arguments map[string]any) (string, bool) {
	return render(template, arguments, locale, true)
}

// render 逐段渲染模板；strict 时任一占位符缺值返回假
func render(template string, arguments map[string]any, locale string, strict bool) (string, bool) {
	var builder strings.Builder
	builder.Grow(len(template) + 16)
	for index := 0; index < len(template); {
		var part segment
		part, index = readSegment(template, index)
		if !part.isPlaceholder {
			builder.WriteString(part.literal)
			continue
		}
		value, ok := arguments[part.name]
		if !ok {
			if strict {
				return "", false
			}
			builder.WriteByte('{')
			builder.WriteString(part.name)
			if part.hasFormat {
				builder.WriteByte(':')
				builder.WriteString(part.format)
			}
			builder.WriteByte('}')
			continue
		}
		builder.WriteString(FormatArgumentIn(locale, value, part.format))
	}
	return builder.String(), true
}

// FormatArgument 按格式说明符写一个参数（RFC-0010「模板」，ADR-0236），按不变文化；见 FormatArgumentIn
func FormatArgument(value any, format string) string { return FormatArgumentIn("", value, format) }

// FormatArgumentIn 按格式说明符与区域写一个参数（RFC-0010「模板」，ADR-0236）。值先归到值模型：空、布尔、字符串、64 位整数、双精度浮点、
// 数组与对象（见 FormatValue）。说明符文本符合 ^[DdFfNnXx][0-9]{0,2}$ 时才认，其余（E2、G、P、0.00、yyyy、空说明符、带空格、D100、D-1）
// 一律按无格式写出：
//   - D[n]：整数，至少 n 位，补零，负号在前；D、F、N 不区分大小写
//   - X[n] / x[n]：非负整数的大写 / 小写十六进制，至少 n 位
//   - F[n]：定点小数，n 位小数，缺省 2；N[n]：同 F，整数部分带千分位
//
// D 与 X 只用于整数，X 只用于非负整数，F 与 N 用于整数与浮点；用在别的值（含超出 64 位、按模型成为浮点的整数，字符串、布尔、null、
// 数组与对象）上按无格式写出。F 与 N 按值的精确十进制展开舍入，正好一半时取偶（0.125 写 F2 得 0.12，2.5 写 F0 得 2），写出全部整数位，
// 负数舍入成零时保留负号（-0.00）。精度是字母后的 0–2 位数字，可以有前导零（F02 即 F2）。
// 区域只改变小数点与分组符（de-DE、es-ES、pt-BR 是 , 与 .，fr-FR 是 , 与 U+202F，ru-RU 是 , 与 U+00A0，其余区域与不给区域是 . 与 ,，
// 区域名不区分大小写）：小数点用在无格式的浮点（含指数写法的尾数）与 F、N 上，分组符只用在 N 上；整数无格式时不分组，D、X 与数组、
// 对象里的数字不受区域影响；负号、NaN 与 Infinity 不随区域变化
func FormatArgumentIn(locale string, value any, format string) string {
	specifier, precision := parseSpecifier(format)
	return valueText(normalize(value), specifier, precision, locale)
}

// parseSpecifier 解析可移植的说明符 ^[DdFfNnXx][0-9]{0,2}$：返回规范化的字母（D、F、N 取大写，X 与 x 保留大小写）与精度
// （没有写出时为 -1）；不合文法时字母为 0，按无格式写出
func parseSpecifier(format string) (byte, int) {
	if len(format) == 0 || len(format) > 3 {
		return 0, -1
	}
	var letter byte
	switch format[0] {
	case 'D', 'd':
		letter = 'D'
	case 'F', 'f':
		letter = 'F'
	case 'N', 'n':
		letter = 'N'
	case 'X', 'x':
		letter = format[0]
	default:
		return 0, -1
	}
	if len(format) == 1 {
		return letter, -1
	}
	precision := 0
	for _, digit := range []byte(format[1:]) {
		if digit < '0' || digit > '9' {
			return 0, -1
		}
		precision = precision*10 + int(digit-'0')
	}
	return letter, precision
}
