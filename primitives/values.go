package primitives

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// FormatValue 返回消息参数的文本形式，模板与 Describe 共用（RFC-0010）：字符串原样，整数十进制，布尔 true / false，
// nil 为空串；浮点在 1e-4 ≤ |x| < 1e17 或 x = 0 时写最短往返的十进制、不用指数，整数值不带小数点（2.0 写 2，1e16 写
// 10000000000000000），范围之外写最短往返的有效数字加 E、符号与至少两位的指数（1E+17、9.9E-05、5E-324），负零写 -0，
// 非数与无穷写 NaN、Infinity、-Infinity（ADR-0235，取自 .NET 10 的实测）。数组与对象不作规定（这里按 fmt.Sprint）
func FormatValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case uintptr:
		return strconv.FormatUint(uint64(typed), 10)
	case float32:
		return formatFloat(float64(typed), 32)
	case float64:
		return formatFloat(typed, 64)
	case json.Number:
		return typed.String()
	}
	// nil 指针与 nil 一样是空串；有 Error / String 方法的用它；底层是基本类型的命名类型按底层类型写
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer && reflected.IsNil() {
		return ""
	}
	switch typed := value.(type) {
	case error:
		return typed.Error()
	case fmt.Stringer:
		return typed.String()
	}
	switch reflected.Kind() {
	case reflect.String:
		return reflected.String()
	case reflect.Bool:
		return strconv.FormatBool(reflected.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(reflected.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(reflected.Uint(), 10)
	case reflect.Float32:
		return formatFloat(reflected.Float(), 32)
	case reflect.Float64:
		return formatFloat(reflected.Float(), 64)
	}
	return fmt.Sprint(value)
}

// formatFloat 写浮点：1e-4 ≤ |x| < 1e17 或 x = 0（含负零）时最短往返、不用指数；之外是 strconv 的最短往返指数形式换成大写 E
// （已带符号与至少两位的指数，与 .NET 的 1E+17、9.9E-05 一致）；NaN 与无穷照 .NET 的不变文化
func formatFloat(value float64, bitSize int) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	}
	if magnitude := math.Abs(value); magnitude == 0 || (magnitude >= 1e-4 && magnitude < 1e17) {
		return strconv.FormatFloat(value, 'f', -1, bitSize)
	}
	return strings.ToUpper(strconv.FormatFloat(value, 'g', -1, bitSize))
}
