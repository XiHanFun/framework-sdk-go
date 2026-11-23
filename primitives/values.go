package primitives

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/xihanfun/framework-sdk-go/internal/jsoncodec"
	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// maxValueDepth 是数组与对象的最大嵌套层数（与 JSON 读取的上限相同）：更深的容器写成 null，自引用的值也因此不会无限递归
const maxValueDepth = 64

// localeSymbols 是区域的小数点与分组符（RFC-0010「模板」，ADR-0236，取自 .NET 10 的 ICU 实测）：区域名不区分大小写，
// 表外的区域与不给区域一样按不变文化（. 与 ,）；en-US、zh-CN、zh-TW、ja-JP、ko-KR 与不变文化相同
var localeSymbols = map[string][2]rune{
	"de-de": {',', '.'},
	"es-es": {',', '.'},
	"pt-br": {',', '.'},
	"fr-fr": {',', rune(0x202F)},
	"ru-ru": {',', rune(0x00A0)},
}

// symbolsOf 返回区域的小数点与分组符
func symbolsOf(locale string) (rune, rune) {
	if symbols, ok := localeSymbols[strings.ToLower(locale)]; ok {
		return symbols[0], symbols[1]
	}
	return '.', ','
}

// container 是值模型里的数组或对象，写出时再展开
type container struct{ value reflect.Value }

// normalize 把参数值归到可移植的值模型（RFC-0010「模板」，ADR-0236）：nil、bool、string、int64、float64，或数组与对象（container）。
//   - 各宽度的整数在 64 位有符号范围内是 int64，超出时（大的 uint64、*big.Int）是 float64；float32 换成最接近的 float64
//     （float32(0.1) 写 0.10000000149011612），*big.Float 与 *big.Rat 也换成 float64
//   - json.Number 与 json.RawMessage 按 JSON 的值模型读：整数写法且在 64 位范围内是 int64，其余数字是 float64
//   - 字符串键与其他键的映射是对象（键写成它的文本），切片与数组是数组；nil 指针、nil 切片与 nil 映射是 null
//   - 模型之外的值是 Go 的文本：error 取 Error()，fmt.Stringer 取 String()（time.Time、time.Duration、实现了 String 的枚举），
//     其余（结构体、复数等）取 fmt.Sprint
func normalize(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case bool, string, int64, float64:
		return typed
	case json.Number:
		// 整个文本是一个 JSON 数字才按数字读：带首尾空白、正号或前导零的不是，按它的文本
		if !jsontext.IsNumber(string(typed)) {
			return string(typed)
		}
		return jsoncodec.Number(string(typed))
	case json.RawMessage:
		root, problem := jsontext.Parse(typed)
		if problem != nil {
			return string(typed)
		}
		return normalize(jsoncodec.FromJSON(root))
	case *big.Int:
		if typed == nil {
			return nil
		}
		if typed.IsInt64() {
			return typed.Int64()
		}
		float, _ := new(big.Float).SetInt(typed).Float64()
		return float
	case *big.Float:
		if typed == nil {
			return nil
		}
		float, _ := typed.Float64()
		return float
	case *big.Rat:
		if typed == nil {
			return nil
		}
		float, _ := typed.Float64()
		return float
	}
	reflected := reflect.ValueOf(value)
	if (reflected.Kind() == reflect.Pointer || reflected.Kind() == reflect.Interface) && reflected.IsNil() {
		return nil
	}
	switch typed := value.(type) {
	case error:
		return typed.Error()
	case fmt.Stringer:
		return typed.String()
	}
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if unsigned := reflected.Uint(); unsigned <= math.MaxInt64 {
			return int64(unsigned)
		}
		return float64(reflected.Uint())
	case reflect.Float32, reflect.Float64:
		return reflected.Float()
	case reflect.String:
		return reflected.String()
	case reflect.Pointer, reflect.Interface:
		if !reflected.Elem().CanInterface() {
			return fmt.Sprint(value)
		}
		return normalize(reflected.Elem().Interface())
	case reflect.Slice, reflect.Map:
		if reflected.IsNil() {
			return nil
		}
		return container{reflected}
	case reflect.Array:
		return container{reflected}
	}
	return fmt.Sprint(value)
}

// FormatValue 返回消息参数的文本形式，模板与 Describe 共用（RFC-0010「模板」）：先按值模型归一（见 FormatArgument），
// 字符串原样，整数十进制，布尔 true / false，nil 为空串；浮点在 1e-4 ≤ |x| < 1e17 或 x = 0 时写最短往返的十进制、不用指数，
// 整数值不带小数点（2.0 写 2），范围之外写 1E+17、9.9E-05、5E-324 这样的指数形式，负零写 -0，非数与无穷写 NaN、Infinity、-Infinity；
// 数组与对象写成紧凑的 JSON 文本，对象成员按名字的 UTF-16 码元序排序（ADR-0236）。按不变文化，不随区域变化
func FormatValue(value any) string { return FormatArgumentIn("", value, "") }

// valueText 写一个已归一的值：容器写成 JSON；标量按说明符与区域写
func valueText(value any, specifier byte, precision int, locale string) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(typed)
	case string:
		return typed
	case int64:
		return localize(integerText(typed, specifier, precision), locale)
	case float64:
		return localize(floatText(typed, specifier, precision), locale)
	case container:
		return string(appendJSON(nil, typed, 1))
	}
	return fmt.Sprint(value)
}

// localize 把不变文化的数字文本换成区域的小数点与分组符：不变文化的文本里 . 只是小数点、, 只是分组符，逐个替换即可
func localize(invariant, locale string) string {
	decimal, group := symbolsOf(locale)
	if decimal == '.' && group == ',' {
		return invariant
	}
	var builder strings.Builder
	for _, c := range invariant {
		switch c {
		case '.':
			builder.WriteRune(decimal)
		case ',':
			builder.WriteRune(group)
		default:
			builder.WriteRune(c)
		}
	}
	return builder.String()
}

// integerText 按说明符写整数（不变文化）：D 至少 precision 位、补零、负号在前；X / x 只用于非负整数；F 与 N 写 precision 位小数（缺省 2），
// N 带千分位；其余（含负数的 X）按无格式写十进制
func integerText(value int64, specifier byte, precision int) string {
	magnitude := uint64(value)
	sign := ""
	if value < 0 {
		magnitude, sign = uint64(-(value+1))+1, "-"
	}
	switch specifier {
	case 'D':
		return sign + padLeft(strconv.FormatUint(magnitude, 10), precision)
	case 'X', 'x':
		if value >= 0 {
			digits := strconv.FormatUint(magnitude, 16)
			if specifier == 'X' {
				digits = strings.ToUpper(digits)
			}
			return padLeft(digits, precision)
		}
	case 'F', 'N':
		if precision < 0 {
			precision = 2
		}
		text := strconv.FormatUint(magnitude, 10)
		if specifier == 'N' {
			text = groupThousands(text)
		}
		if precision > 0 {
			text += "." + strings.Repeat("0", precision)
		}
		return sign + text
	}
	return strconv.FormatInt(value, 10)
}

// floatText 按说明符写浮点（不变文化）：F 与 N 按值的精确十进制展开舍入、正好一半时取偶（strconv 与 .NET 相同），写出全部整数位，
// N 带千分位；D 与 X 不适用于浮点，与其余说明符一样按无格式写出；非数与无穷总是 NaN、Infinity、-Infinity
func floatText(value float64, specifier byte, precision int) string {
	if (specifier == 'F' || specifier == 'N') && !math.IsNaN(value) && !math.IsInf(value, 0) {
		if precision < 0 {
			precision = 2
		}
		text := strconv.FormatFloat(value, 'f', precision, 64)
		if specifier == 'N' {
			sign := ""
			if strings.HasPrefix(text, "-") {
				sign, text = "-", text[1:]
			}
			integer, fraction, _ := strings.Cut(text, ".")
			text = sign + groupThousands(integer)
			if fraction != "" {
				text += "." + fraction
			}
		}
		return text
	}
	return jsoncodec.FloatText(value)
}

// padLeft 不足 width 位时在左边补零
func padLeft(digits string, width int) string {
	if len(digits) < width {
		return strings.Repeat("0", width-len(digits)) + digits
	}
	return digits
}

// groupThousands 给十进制整数文本加千分位逗号
func groupThousands(digits string) string {
	var builder strings.Builder
	for index, digit := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			builder.WriteByte(',')
		}
		builder.WriteRune(digit)
	}
	return builder.String()
}

// appendJSON 把值写成紧凑的 JSON 文本（RFC-0010「模板」，ADR-0236）：没有空白，对象成员按名字的 UTF-16 码元序排序、同名保持原顺序，
// null 写 null，数字与顶层的无格式写法相同、不随区域变化，字符串按 RFC-0010 的 JSON 字符串写法；depth 是这个值作为容器时的层数，
// 超过 64 层的容器写成 null
func appendJSON(buffer []byte, value any, depth int) []byte {
	normalized := value
	if _, ok := value.(container); !ok {
		normalized = normalize(value)
	}
	switch typed := normalized.(type) {
	case nil:
		return append(buffer, "null"...)
	case bool:
		return strconv.AppendBool(buffer, typed)
	case string:
		return jsontext.AppendString(buffer, typed)
	case int64:
		return strconv.AppendInt(buffer, typed, 10)
	case float64:
		return append(buffer, jsoncodec.FloatText(typed)...)
	case container:
		if depth > maxValueDepth {
			return append(buffer, "null"...)
		}
		if typed.value.Kind() == reflect.Map {
			return appendObject(buffer, typed.value, depth)
		}
		buffer = append(buffer, '[')
		for index := 0; index < typed.value.Len(); index++ {
			if index > 0 {
				buffer = append(buffer, ',')
			}
			buffer = appendJSON(buffer, element(typed.value.Index(index)), depth+1)
		}
		return append(buffer, ']')
	}
	return jsontext.AppendString(buffer, fmt.Sprint(normalized))
}

// appendObject 写一个对象：键写成它的文本，成员按名字的 UTF-16 码元序稳定排序
func appendObject(buffer []byte, value reflect.Value, depth int) []byte {
	type member struct {
		name  string
		value any
	}
	members := make([]member, 0, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		members = append(members, member{name: FormatValue(element(iterator.Key())), value: element(iterator.Value())})
	}
	slices.SortStableFunc(members, func(left, right member) int { return compareUTF16(left.name, right.name) })
	buffer = append(buffer, '{')
	for index, item := range members {
		if index > 0 {
			buffer = append(buffer, ',')
		}
		buffer = jsontext.AppendString(buffer, item.name)
		buffer = append(buffer, ':')
		buffer = appendJSON(buffer, item.value, depth+1)
	}
	return append(buffer, '}')
}

// element 取容器里的一项；不能取接口的值（未导出类型的字段）按它的文本
func element(value reflect.Value) any {
	if !value.CanInterface() {
		return fmt.Sprint(value)
	}
	return value.Interface()
}
