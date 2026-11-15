package primitives

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// placeholderNamePattern 是占位符名文法（RFC-0010）：字母开头的字母、数字与下划线（按名字），或纯数字（按位置）
var placeholderNamePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*|[0-9]+)$`)

// maxFormatPrecision 是格式说明符精度的上限：更大的精度按不支持的说明符处理，不生成过长的文本
const maxFormatPrecision = 999

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
// {{ 与 }} 是转义；缺值的占位符连同 :format 原样保留，让遗漏可见
func FormatMessage(template string, arguments map[string]any) string {
	text, _ := render(template, arguments, false)
	return text
}

// TryFormatMessage 严格格式化模板：任一占位符缺值返回假，供消息目录拒绝半截译文、调用方退回 Describe
func TryFormatMessage(template string, arguments map[string]any) (string, bool) {
	return render(template, arguments, true)
}

// render 逐段渲染模板；strict 时任一占位符缺值返回假
func render(template string, arguments map[string]any, strict bool) (string, bool) {
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
		builder.WriteString(FormatArgument(value, part.format))
	}
	return builder.String(), true
}

// FormatArgument 按格式说明符写一个参数（RFC-0010 的可移植子集）：
//   - D[n]：整数，最少 n 位，补零，负号在前
//   - X[n] / x[n]：非负整数的大写 / 小写十六进制，最少 n 位
//   - F[n]：定点小数，n 位小数，缺省 2
//   - N[n]：千分位用 ","、小数点用 "."，n 位小数，缺省 2，负号在前
//
// F 与 N 按值的精确十进制展开舍入，正好一半时取偶（四舍六入五成双，与 .NET 一致）：0.125 写 F2 得 0.12，0.375 得 0.38，
// 2.5 写 F0 得 2、3.5 得 4、-2.5 得 -2；2.675 的二进制值略小于 2.675，不是正好一半，写 F2 得 2.67。
// 空说明符、子集之外的说明符（日期、自定义数字模式等 .NET 格式串）与值不适用的说明符按无格式输出，即 FormatValue
func FormatArgument(value any, format string) string {
	specifier, precision, ok := parseFormat(format)
	if !ok {
		return FormatValue(value)
	}
	switch specifier {
	case 'D':
		if integer, ok := integerOf(value); ok {
			return padDigits(integer, 10, precision, false)
		}
	case 'X', 'x':
		if integer, ok := integerOf(value); ok && integer.Sign() >= 0 {
			return padDigits(integer, 16, precision, specifier == 'X')
		}
	case 'F', 'N':
		if precision < 0 {
			precision = 2
		}
		if rational, ok := rationalOf(value); ok {
			text := formatFixed(rational, precision)
			if specifier == 'N' {
				text = groupThousands(text)
			}
			return text
		}
	}
	return FormatValue(value)
}

// parseFormat 解析可移植子集的说明符：一个字母（D、X、x、F、N）后跟可选的十进制精度；精度缺省为 -1
func parseFormat(format string) (byte, int, bool) {
	if format == "" || !strings.ContainsRune("DXxFN", rune(format[0])) {
		return 0, 0, false
	}
	if len(format) == 1 {
		return format[0], -1, true
	}
	digits := format[1:]
	for index := 0; index < len(digits); index++ {
		if digits[index] < '0' || digits[index] > '9' {
			return 0, 0, false
		}
	}
	precision, err := strconv.Atoi(digits)
	if err != nil || precision > maxFormatPrecision {
		return 0, 0, false
	}
	return format[0], precision, true
}

// padDigits 写整数的 base 进制数字，不足 precision 位时补零，负号在前
func padDigits(integer *big.Int, base, precision int, upper bool) string {
	digits := new(big.Int).Abs(integer).Text(base)
	if upper {
		digits = strings.ToUpper(digits)
	}
	if len(digits) < precision {
		digits = strings.Repeat("0", precision-len(digits)) + digits
	}
	if integer.Sign() < 0 {
		return "-" + digits
	}
	return digits
}

// formatFixed 写 precision 位小数的定点小数：按精确值舍入，正好一半时取偶，负号在前（舍入成零的负数写 -0.00，与 strconv 一致）
func formatFixed(value *big.Rat, precision int) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(precision)), nil)
	numerator := new(big.Int).Mul(new(big.Int).Abs(value.Num()), scale)
	quotient, remainder := new(big.Int).QuoRem(numerator, value.Denom(), new(big.Int))
	switch remainder.Lsh(remainder, 1).Cmp(value.Denom()) {
	case 1:
		quotient.Add(quotient, big.NewInt(1))
	case 0:
		if quotient.Bit(0) == 1 {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	digits := quotient.String()
	if precision > 0 {
		if len(digits) <= precision {
			digits = strings.Repeat("0", precision-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-precision] + "." + digits[len(digits)-precision:]
	}
	if value.Sign() < 0 {
		return "-" + digits
	}
	return digits
}

// groupThousands 给定点小数文本的整数部分加千分位逗号
func groupThousands(text string) string {
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}
	integer, fraction := text, ""
	if point := strings.IndexByte(text, '.'); point >= 0 {
		integer, fraction = text[:point], text[point:]
	}
	var builder strings.Builder
	for index, digit := range integer {
		if index > 0 && (len(integer)-index)%3 == 0 {
			builder.WriteByte(',')
		}
		builder.WriteRune(digit)
	}
	return sign + builder.String() + fraction
}

// integerOf 取整数值：整数类型（含命名类型），以及值为整数的有限浮点与 json.Number（JSON 线上的数字不分整数与浮点）
func integerOf(value any) (*big.Int, bool) {
	switch typed := value.(type) {
	case json.Number:
		if integer, ok := new(big.Int).SetString(typed.String(), 10); ok {
			return integer, true
		}
		if rational, ok := new(big.Rat).SetString(typed.String()); ok && rational.IsInt() {
			return rational.Num(), true
		}
		return nil, false
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return big.NewInt(reflected.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return new(big.Int).SetUint64(reflected.Uint()), true
	case reflect.Float32, reflect.Float64:
		float := reflected.Float()
		if math.IsInf(float, 0) || math.IsNaN(float) || float != math.Trunc(float) {
			return nil, false
		}
		integer, _ := big.NewFloat(float).Int(nil)
		return integer, true
	}
	return nil, false
}

// rationalOf 取数值的精确有理数：整数、有限浮点（精确的二进制值）与 json.Number（精确的十进制值）
func rationalOf(value any) (*big.Rat, bool) {
	if number, ok := value.(json.Number); ok {
		return new(big.Rat).SetString(number.String())
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return new(big.Rat).SetInt64(reflected.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return new(big.Rat).SetUint64(reflected.Uint()), true
	case reflect.Float32, reflect.Float64:
		float := reflected.Float()
		if math.IsInf(float, 0) || math.IsNaN(float) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(float), true
	}
	return nil, false
}
