package jsoncodec

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

var (
	unmarshalerType     = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// Problem 是解码不合请求类型的第一处
type Problem struct {
	// Path 是该处的路径（RFC-0011 规则 2）：根上就不合时是 $，缺少必填成员时是所在对象的路径
	Path string
	// Cause 是自定义解码（UnmarshalJSON / UnmarshalText）返回的错误；类型不符与缺少成员时为空
	Cause error
}

// Error 便于日志
func (p *Problem) Error() string {
	if p.Cause != nil {
		return "the value at '" + p.Path + "' could not be decoded: " + p.Cause.Error()
	}
	return "the value at '" + p.Path + "' does not match the type"
}

// Unwrap 返回自定义解码的错误
func (p *Problem) Unwrap() error { return p.Cause }

// Unmarshal 读一段 JSON 文本：先过共同检查（语法错误与重复成员返回 *jsontext.Problem），再按 target 的类型解码（返回 *Problem）
func Unmarshal(data []byte, target any) error {
	root, problem := jsontext.Parse(data)
	if problem != nil {
		return problem
	}
	if problem := Decode(data, root, target); problem != nil {
		return problem
	}
	return nil
}

// Decode 把已过共同检查的值解码到 target（非 nil 指针）里，data 是读出这个值的原文。规则（RFC-0011 规则 2，照 System.Text.Json 的缺省读取选项）：
//   - 成员名区分大小写，未知成员忽略；数字不从字符串读，字符串也不从数字读（字段带 json:",string" 时除外）
//   - 整数不接受小数点与指数（1.0、1e2 都不是整数），超出类型的范围即不符；浮点接受整数写法，上溢为无穷，下溢为 0
//   - null：字符串、结构体、切片、数组、映射、指针与接口接受，等于没有值；整数、浮点与布尔不接受
//   - 带 xihan:"required" 标签的成员缺少时在所在对象结束时报该对象的路径；值为 null 也算出现
//   - 按文本顺序报第一处问题；实现 json.Unmarshaler 的类型拿到原文自己解码，实现 encoding.TextUnmarshaler 的类型拿到字符串
//   - 空接口按值模型（整数写法在 64 位范围内为 int64，其余数字为 float64）
func Decode(data []byte, value *jsontext.Value, target any) *Problem {
	pointer := reflect.ValueOf(target)
	if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return &Problem{Path: "$", Cause: errors.New("the decoding target must be a non-nil pointer")}
	}
	return (&decoder{data: data}).value(value, pointer.Elem(), "$", false)
}

// decoder 是一次解码
type decoder struct {
	data []byte
}

// raw 返回值的原文
func (d *decoder) raw(value *jsontext.Value) []byte { return d.data[value.Start:value.End] }

// value 解码一个值；quoted 为真时标量可以写在字符串里（json:",string"）
func (d *decoder) value(value *jsontext.Value, target reflect.Value, path string, quoted bool) *Problem {
	if value.Kind == jsontext.Null {
		return d.null(value, target, path)
	}
	for target.Kind() == reflect.Pointer {
		if target.IsNil() {
			if !target.CanSet() {
				return &Problem{Path: path}
			}
			target.Set(reflect.New(target.Type().Elem()))
		}
		if handled, problem := d.custom(value, target, path); handled {
			return problem
		}
		target = target.Elem()
	}
	if target.CanAddr() {
		if handled, problem := d.custom(value, target.Addr(), path); handled {
			return problem
		}
	}
	if quoted && value.Kind == jsontext.String {
		return d.quoted(value, target, path)
	}
	switch target.Kind() {
	case reflect.Bool:
		if value.Kind != jsontext.Bool {
			return &Problem{Path: path}
		}
		target.SetBool(value.Bool)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Kind != jsontext.Number || !isIntegerText(value.Text) {
			return &Problem{Path: path}
		}
		integer, err := strconv.ParseInt(value.Text, 10, target.Type().Bits())
		if err != nil {
			return &Problem{Path: path}
		}
		target.SetInt(integer)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if value.Kind != jsontext.Number || !isIntegerText(value.Text) {
			return &Problem{Path: path}
		}
		unsigned, err := strconv.ParseUint(value.Text, 10, target.Type().Bits())
		if err != nil {
			return &Problem{Path: path}
		}
		target.SetUint(unsigned)
	case reflect.Float32, reflect.Float64:
		if value.Kind != jsontext.Number {
			return &Problem{Path: path}
		}
		target.SetFloat(parseFloat(value.Text, target.Type().Bits()))
	case reflect.String:
		if target.Type() == numberType {
			text := value.Text
			if value.Kind == jsontext.String {
				if !validNumber(text) {
					return &Problem{Path: path}
				}
			} else if value.Kind != jsontext.Number {
				return &Problem{Path: path}
			}
			target.SetString(text)
			return nil
		}
		if value.Kind != jsontext.String {
			return &Problem{Path: path}
		}
		target.SetString(value.Text)
	case reflect.Interface:
		if target.NumMethod() != 0 {
			return &Problem{Path: path}
		}
		target.Set(reflect.ValueOf(FromJSON(value)))
	case reflect.Slice:
		return d.slice(value, target, path)
	case reflect.Array:
		if value.Kind != jsontext.Array {
			return &Problem{Path: path}
		}
		for index := 0; index < target.Len(); index++ {
			if index >= len(value.Items) {
				target.Index(index).SetZero()
				continue
			}
			if problem := d.value(value.Items[index], target.Index(index), jsontext.IndexPath(path, index), false); problem != nil {
				return problem
			}
		}
	case reflect.Map:
		return d.mapping(value, target, path)
	case reflect.Struct:
		return d.structure(value, target, path)
	default:
		return &Problem{Path: path}
	}
	return nil
}

// null 解码 null：可空的种类置空或保持原样，整数、浮点与布尔不接受
func (d *decoder) null(value *jsontext.Value, target reflect.Value, path string) *Problem {
	switch target.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map:
		if target.CanSet() {
			target.SetZero()
		}
		return nil
	}
	if target.CanAddr() {
		if handled, problem := d.custom(value, target.Addr(), path); handled {
			return problem
		}
	}
	switch target.Kind() {
	case reflect.String, reflect.Struct, reflect.Array:
		return nil
	}
	return &Problem{Path: path}
}

// custom 把值交给类型自己的 UnmarshalJSON（拿到原文）或 UnmarshalText（只接字符串，null 等于没有值）；pointer 是指向目标的指针
func (d *decoder) custom(value *jsontext.Value, pointer reflect.Value, path string) (bool, *Problem) {
	if pointer.Type().NumMethod() == 0 || !pointer.CanInterface() {
		return false, nil
	}
	switch typed := pointer.Interface().(type) {
	case json.Unmarshaler:
		if err := typed.UnmarshalJSON(d.raw(value)); err != nil {
			return true, &Problem{Path: problemPath(path, err), Cause: err}
		}
		return true, nil
	case encoding.TextUnmarshaler:
		switch value.Kind {
		case jsontext.Null:
			return true, nil
		case jsontext.String:
			if err := typed.UnmarshalText([]byte(value.Text)); err != nil {
				return true, &Problem{Path: path, Cause: err}
			}
			return true, nil
		}
		return true, &Problem{Path: path}
	}
	return false, nil
}

// problemPath 让嵌套的自定义解码报出的路径接在外层路径之后（如 Result 里的值）：自己的 *Problem 带的是从 $ 起的相对路径
func problemPath(path string, err error) string {
	var nested *Problem
	if errors.As(err, &nested) && len(nested.Path) > 0 && nested.Path[0] == '$' {
		return path + nested.Path[1:]
	}
	return path
}

// quoted 解码写在字符串里的标量（json:",string"）
func (d *decoder) quoted(value *jsontext.Value, target reflect.Value, path string) *Problem {
	inner, problem := jsontext.Parse([]byte(value.Text))
	if problem != nil || inner.Kind == jsontext.Array || inner.Kind == jsontext.Object || inner.Kind == jsontext.String {
		return &Problem{Path: path}
	}
	return (&decoder{data: []byte(value.Text)}).value(inner, target, path, false)
}

// slice 解码切片；[]byte 也接受 base64 字符串（与 encoding/json 一样）
func (d *decoder) slice(value *jsontext.Value, target reflect.Value, path string) *Problem {
	if value.Kind == jsontext.String && target.Type().Elem().Kind() == reflect.Uint8 {
		decoded, err := base64.StdEncoding.DecodeString(value.Text)
		if err != nil {
			return &Problem{Path: path, Cause: err}
		}
		target.SetBytes(decoded)
		return nil
	}
	if value.Kind != jsontext.Array {
		return &Problem{Path: path}
	}
	items := reflect.MakeSlice(target.Type(), len(value.Items), len(value.Items))
	for index, item := range value.Items {
		if problem := d.value(item, items.Index(index), jsontext.IndexPath(path, index), false); problem != nil {
			return problem
		}
	}
	target.Set(items)
	return nil
}

// mapping 解码映射：值按成员的文本顺序；键是字符串、encoding.TextUnmarshaler 或整数
func (d *decoder) mapping(value *jsontext.Value, target reflect.Value, path string) *Problem {
	if value.Kind != jsontext.Object {
		return &Problem{Path: path}
	}
	mapType := target.Type()
	if target.IsNil() {
		target.Set(reflect.MakeMapWithSize(mapType, len(value.Members)))
	}
	for _, member := range value.Members {
		memberPath := jsontext.MemberPath(path, member.Name)
		key, err := mapKeyOf(mapType.Key(), member.Name)
		if err != nil {
			return &Problem{Path: memberPath, Cause: err}
		}
		element := reflect.New(mapType.Elem()).Elem()
		if problem := d.value(member.Value, element, memberPath, false); problem != nil {
			return problem
		}
		target.SetMapIndex(key, element)
	}
	return nil
}

// mapKeyOf 把成员名换成映射的键
func mapKeyOf(keyType reflect.Type, name string) (reflect.Value, error) {
	if reflect.PointerTo(keyType).Implements(textUnmarshalerType) {
		key := reflect.New(keyType)
		if err := key.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(name)); err != nil {
			return reflect.Value{}, err
		}
		return key.Elem(), nil
	}
	switch keyType.Kind() {
	case reflect.String:
		return reflect.ValueOf(name).Convert(keyType), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer, err := strconv.ParseInt(name, 10, keyType.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(integer).Convert(keyType), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		unsigned, err := strconv.ParseUint(name, 10, keyType.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(unsigned).Convert(keyType), nil
	}
	return reflect.Value{}, fmt.Errorf("the map key type %s cannot be read from JSON", keyType)
}

// structure 解码结构体：成员按文本顺序、名字区分大小写，未知成员忽略；成员都读完后核对必填成员，缺少时报对象自己的路径
func (d *decoder) structure(value *jsontext.Value, target reflect.Value, path string) *Problem {
	if value.Kind != jsontext.Object {
		return &Problem{Path: path}
	}
	fields := fieldsOf(target.Type())
	var seen []bool
	for _, member := range value.Members {
		position := -1
		for index := range fields {
			if fields[index].name == member.Name {
				position = index
				break
			}
		}
		if position < 0 {
			continue
		}
		if seen == nil {
			seen = make([]bool, len(fields))
		}
		seen[position] = true
		memberPath := jsontext.MemberPath(path, member.Name)
		field, ok := settableField(target, fields[position].index)
		if !ok {
			return &Problem{Path: memberPath, Cause: errors.New("the embedded pointer to an unexported struct cannot be set")}
		}
		if problem := d.value(member.Value, field, memberPath, fields[position].quoted); problem != nil {
			return problem
		}
	}
	for index, field := range fields {
		if field.required && (seen == nil || !seen[index]) {
			return &Problem{Path: path}
		}
	}
	return nil
}

// settableField 按下标取字段，途经的 nil 嵌入指针先分配；未导出的嵌入指针分配不了
func settableField(target reflect.Value, index []int) (reflect.Value, bool) {
	for position, step := range index {
		if position > 0 && target.Kind() == reflect.Pointer {
			if target.IsNil() {
				if !target.CanSet() {
					return reflect.Value{}, false
				}
				target.Set(reflect.New(target.Type().Elem()))
			}
			target = target.Elem()
		}
		target = target.Field(step)
	}
	return target, true
}

// validNumber 判断文本是不是合法的 JSON 数字
func validNumber(text string) bool {
	value, problem := jsontext.Parse([]byte(text))
	return problem == nil && value.Kind == jsontext.Number && len(text) == value.End-value.Start
}
