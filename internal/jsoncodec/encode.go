package jsoncodec

import (
	"bytes"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// maxEncodeDepth 是写出时的最大嵌套层数，自引用的值到这里报错，不无限递归
const maxEncodeDepth = 1000

// FloatText 写双精度浮点的值文本（RFC-0010「模板」，ADR-0235）：1e-4 ≤ |x| < 1e17 或 x = 0（含负零）时写最短往返的十进制、
// 不用指数，范围之外写最短往返的有效数字加 E、符号与至少两位的指数（1E+17、9.9E-05、5E-324）；非数与无穷写 NaN、Infinity、-Infinity
func FloatText(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	}
	if magnitude := math.Abs(value); magnitude == 0 || (magnitude >= 1e-4 && magnitude < 1e17) {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	return strings.ToUpper(strconv.FormatFloat(value, 'g', -1, 64))
}

// appendFloat 写线上的浮点（RFC-0001「Result」，ADR-0236）：整数值的浮点带 .0（2.0、-0.0），对方按值模型读回的仍是浮点；
// 非有限值在 JSON 里写不出，写成字符串 NaN、Infinity、-Infinity，与模板里的文本相同
func appendFloat(dst []byte, value float64) []byte {
	text := FloatText(value)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return jsontext.AppendString(dst, text)
	}
	dst = append(dst, text...)
	if !strings.ContainsAny(text, ".E") {
		dst = append(dst, '.', '0')
	}
	return dst
}

var (
	marshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	numberType        = reflect.TypeFor[json.Number]()
)

// Marshal 按值模型写出一个值（Result 的值、Error 的细节，RFC-0001「Result」，ADR-0236）：整数原样（超出 int64 的无符号整数按浮点写），
// float32 先换成 float64，浮点带小数点或指数，非有限值写成字符串；字符串只转义必须转义的字符（RFC-0010 的 JSON 字符串写法）；
// 结构体、映射、切片与 json.Marshaler / encoding.TextMarshaler 照 encoding/json 的规则
func Marshal(value any) ([]byte, error) {
	var encoder encoder
	if err := encoder.value(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	return encoder.buffer, nil
}

// encoder 是写出的缓冲
type encoder struct {
	buffer []byte
}

// value 写一个值
func (e *encoder) value(value reflect.Value, depth int) error {
	if depth > maxEncodeDepth {
		return fmt.Errorf("the value is nested more than %d levels deep (a cycle?)", maxEncodeDepth)
	}
	if !value.IsValid() {
		e.buffer = append(e.buffer, "null"...)
		return nil
	}
	if handled, err := e.marshaler(value); handled {
		return err
	}
	switch value.Kind() {
	case reflect.Bool:
		e.buffer = strconv.AppendBool(e.buffer, value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.buffer = strconv.AppendInt(e.buffer, value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if unsigned := value.Uint(); unsigned <= math.MaxInt64 {
			e.buffer = strconv.AppendUint(e.buffer, unsigned, 10)
		} else {
			e.buffer = appendFloat(e.buffer, float64(unsigned))
		}
	case reflect.Float32, reflect.Float64:
		e.buffer = appendFloat(e.buffer, value.Float())
	case reflect.String:
		if value.Type() == numberType {
			text := value.String()
			if text == "" {
				text = "0"
			}
			e.buffer = append(e.buffer, text...)
			return nil
		}
		e.buffer = jsontext.AppendString(e.buffer, value.String())
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			e.buffer = append(e.buffer, "null"...)
			return nil
		}
		return e.value(value.Elem(), depth+1)
	case reflect.Slice:
		if value.IsNil() {
			e.buffer = append(e.buffer, "null"...)
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 && !value.Type().Elem().Implements(marshalerType) && !value.Type().Elem().Implements(textMarshalerType) {
			e.buffer = jsontext.AppendString(e.buffer, base64.StdEncoding.EncodeToString(value.Bytes()))
			return nil
		}
		return e.array(value, depth)
	case reflect.Array:
		return e.array(value, depth)
	case reflect.Map:
		if value.IsNil() {
			e.buffer = append(e.buffer, "null"...)
			return nil
		}
		return e.mapping(value, depth)
	case reflect.Struct:
		return e.structure(value, depth)
	default:
		return fmt.Errorf("the type %s cannot be written as JSON", value.Type())
	}
	return nil
}

// marshaler 交给值自己的 MarshalJSON 或 MarshalText；指针接收者的方法只在值可寻址时用（与 encoding/json 一样）
func (e *encoder) marshaler(value reflect.Value) (bool, error) {
	// 经未导出的嵌入结构体提升的字段不能调方法，按种类写
	if !value.CanInterface() || (value.Kind() == reflect.Pointer && value.IsNil()) {
		return false, nil
	}
	candidate := value
	if !value.Type().Implements(marshalerType) && !value.Type().Implements(textMarshalerType) {
		if value.Kind() == reflect.Pointer || !value.CanAddr() {
			return false, nil
		}
		candidate = value.Addr()
	}
	if marshaler, ok := candidate.Interface().(json.Marshaler); ok {
		encoded, err := marshaler.MarshalJSON()
		if err != nil {
			return true, err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, encoded); err != nil {
			return true, fmt.Errorf("the MarshalJSON of %s wrote invalid JSON: %w", value.Type(), err)
		}
		e.buffer = append(e.buffer, compact.Bytes()...)
		return true, nil
	}
	if marshaler, ok := candidate.Interface().(encoding.TextMarshaler); ok {
		text, err := marshaler.MarshalText()
		if err != nil {
			return true, err
		}
		e.buffer = jsontext.AppendString(e.buffer, string(text))
		return true, nil
	}
	return false, nil
}

// array 写数组与切片
func (e *encoder) array(value reflect.Value, depth int) error {
	e.buffer = append(e.buffer, '[')
	for index := 0; index < value.Len(); index++ {
		if index > 0 {
			e.buffer = append(e.buffer, ',')
		}
		if err := e.value(value.Index(index), depth+1); err != nil {
			return err
		}
	}
	e.buffer = append(e.buffer, ']')
	return nil
}

// mapping 写映射：键是字符串、encoding.TextMarshaler 或整数，按键的文本排序（与 encoding/json 一样）
func (e *encoder) mapping(value reflect.Value, depth int) error {
	type entry struct {
		name  string
		value reflect.Value
	}
	entries := make([]entry, 0, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		name, err := mapKey(iterator.Key())
		if err != nil {
			return err
		}
		entries = append(entries, entry{name: name, value: iterator.Value()})
	}
	slices.SortFunc(entries, func(left, right entry) int { return strings.Compare(left.name, right.name) })
	e.buffer = append(e.buffer, '{')
	for index, item := range entries {
		if index > 0 {
			e.buffer = append(e.buffer, ',')
		}
		e.buffer = jsontext.AppendString(e.buffer, item.name)
		e.buffer = append(e.buffer, ':')
		if err := e.value(item.value, depth+1); err != nil {
			return err
		}
	}
	e.buffer = append(e.buffer, '}')
	return nil
}

// mapKey 写映射的键
func mapKey(key reflect.Value) (string, error) {
	if key.Kind() == reflect.String {
		return key.String(), nil
	}
	if marshaler, ok := key.Interface().(encoding.TextMarshaler); ok {
		if key.Kind() == reflect.Pointer && key.IsNil() {
			return "", nil
		}
		text, err := marshaler.MarshalText()
		return string(text), err
	}
	switch key.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(key.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(key.Uint(), 10), nil
	}
	return "", fmt.Errorf("the map key type %s cannot be written as JSON", key.Type())
}

// structure 写结构体：成员按声明顺序，omitempty 与 omitzero 照 encoding/json，string 选项把标量写成字符串
func (e *encoder) structure(value reflect.Value, depth int) error {
	e.buffer = append(e.buffer, '{')
	written := false
	for _, member := range fieldsOf(value.Type()) {
		current, ok := fieldValue(value, member.index)
		if !ok {
			continue
		}
		if member.omitEmpty && isEmpty(current) {
			continue
		}
		if member.omitZero && isZero(current) {
			continue
		}
		if written {
			e.buffer = append(e.buffer, ',')
		}
		written = true
		e.buffer = jsontext.AppendString(e.buffer, member.name)
		e.buffer = append(e.buffer, ':')
		if member.quoted && !(current.Kind() == reflect.Pointer && current.IsNil()) {
			var scalar encoder
			if err := scalar.value(current, depth+1); err != nil {
				return err
			}
			e.buffer = jsontext.AppendString(e.buffer, string(scalar.buffer))
			continue
		}
		if err := e.value(current, depth+1); err != nil {
			return err
		}
	}
	e.buffer = append(e.buffer, '}')
	return nil
}

// fieldValue 按下标取字段；途经的嵌入指针为 nil 时没有这个字段
func fieldValue(value reflect.Value, index []int) (reflect.Value, bool) {
	for position, step := range index {
		if position > 0 && value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return reflect.Value{}, false
			}
			value = value.Elem()
		}
		value = value.Field(step)
	}
	return value, true
}

// isEmpty 是 omitempty 的判定
func isEmpty(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return value.IsZero()
	}
	return false
}

// isZero 是 omitzero 的判定：有 IsZero() bool 方法的用它
func isZero(value reflect.Value) bool {
	if !value.CanInterface() {
		return value.IsZero()
	}
	if zeroer, ok := value.Interface().(interface{ IsZero() bool }); ok {
		if value.Kind() == reflect.Pointer && value.IsNil() {
			return true
		}
		return zeroer.IsZero()
	}
	return value.IsZero()
}
