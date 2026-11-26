package jsoncodec

import (
	"encoding"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// zeroerType 是 omitzero 认的 IsZero() bool 方法
var zeroerType = reflect.TypeFor[interface{ IsZero() bool }]()

// wrapped 认出 MarshalJSON 只把里面一个值交给 Marshal 写出的类型（primitives.ID、primitives.Maybe），返回那个值；不是这类值时 ok 为假，
// 没有值（Maybe 的 None 写 null）时 inner 为 nil。由 primitives 在初始化时经 SetWrapped 设置：这两个类型在 primitives 里，本包不能引用它
var wrapped func(value any) (inner any, ok bool)

// SetWrapped 设置 CheckStrings 往里走的包装类型（primitives.ID、primitives.Maybe）；只在 primitives 的 init 里调用
func SetWrapped(unwrap func(value any) (inner any, ok bool)) { wrapped = unwrap }

// invalidString 是 CheckStrings 找到的、不是合法 UTF-8 的字符串：segments 是倒序的路径段，出错之后逐层往外补上，没有问题时不拼路径
type invalidString struct {
	// member 为真表示是成员名（映射的键），路径是它所在的对象
	member   bool
	segments []string
}

// Error 写出位置；不带字符串本身，它不是合法的文本，也可能是不该进日志的内容
func (e *invalidString) Error() string {
	path := "$"
	for index := len(e.segments) - 1; index >= 0; index-- {
		path += e.segments[index]
	}
	if e.member {
		return fmt.Sprintf("a member name in '%s' is not valid UTF-8", path)
	}
	return fmt.Sprintf("the string at '%s' is not valid UTF-8", path)
}

// within 给 CheckStrings 的错误补上外面一层的路径段
func within(err error, segment string) error {
	if invalid, ok := err.(*invalidString); ok {
		invalid.segments = append(invalid.segments, segment)
	}
	return err
}

// CheckStrings 沿 encoding/json 写出 value 时走的路径，核对要写出的每个字符串都是合法的 UTF-8（RFC-0001「写出与转交」、
// RFC-0011 规则 10「编码」，ADR-0318）。encoding/json 把字符串、映射的键与 MarshalText 的结果里非法的 UTF-8（含编成 UTF-8 的
// 孤立代理项）换成 U+FFFD，写出的仍是合法的 JSON，写完之后看不出原来是坏的，所以照它选写法的规则另走一遍：
//   - 方法按 encoding/json 的先后：可寻址时指针的 MarshalJSON、值的 MarshalJSON、可寻址时指针的 MarshalText、值的 MarshalText；
//     MarshalText 的结果要是合法的 UTF-8
//   - 字符串（json.Number 除外，encoding/json 自己核对它是数字）、映射的键（字符串、MarshalText 或整数）与值、切片（[]byte 写成
//     base64，不看）、数组、接口与指针
//   - 结构体按 fieldsOf 的成员（导出字段与提升的嵌入字段，同层同名的都不写，json:"-" 不写）；omitzero 略去的成员不看，
//     omitempty 略去的都是空值，里面没有字符串
//   - json.Marshaler 交出的是 JSON 原文，不往里走：原文里的非法字节、孤立代理项的转义由调用方对写出的整段文本做共同检查
//     （jsontext.Check），MarshalJSON 自己换成 U+FFFD 的这里看不出来；primitives.ID 与 primitives.Maybe 例外，往里走到它们写出的值
//
// MarshalText 与 IsZero 会再被调用一次。调用方先让 encoding/json 写成功、再核对，写得出的值没有循环；层数仍有上限，不无限递归。
// 没有问题时返回 nil，否则返回写明位置的错误
func CheckStrings(value any) error {
	return checkValue(reflect.ValueOf(value), 0)
}

// checkValue 核对一个值
func checkValue(value reflect.Value, depth int) error {
	if depth > maxEncodeDepth {
		return fmt.Errorf("the value is nested more than %d levels deep (a cycle?)", maxEncodeDepth)
	}
	if !value.IsValid() {
		return nil
	}
	t := value.Type()
	// 经未导出的字段取到的值不能调方法：encoding/json 在这里会 panic、写不成，能走到这一步的值都不是这样，按种类看
	if value.CanInterface() {
		addressable := t.Kind() != reflect.Pointer && value.CanAddr()
		switch {
		case addressable && reflect.PointerTo(t).Implements(marshalerType):
			return checkMarshaled(value.Addr(), depth)
		case t.Implements(marshalerType):
			return checkMarshaled(value, depth)
		case addressable && reflect.PointerTo(t).Implements(textMarshalerType):
			return checkText(value.Addr())
		case t.Implements(textMarshalerType):
			return checkText(value)
		}
	}
	switch value.Kind() {
	case reflect.String:
		if t != numberType && !utf8.ValidString(value.String()) {
			return &invalidString{}
		}
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			return checkValue(value.Elem(), depth+1)
		}
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		// 与 encoding/json 相同：元素是字节、它的指针既不是 Marshaler 也不是 TextMarshaler 时写成 base64
		element := reflect.PointerTo(t.Elem())
		if t.Elem().Kind() == reflect.Uint8 && !element.Implements(marshalerType) && !element.Implements(textMarshalerType) {
			return nil
		}
		return checkItems(value, depth)
	case reflect.Array:
		return checkItems(value, depth)
	case reflect.Map:
		return checkMap(value, depth)
	case reflect.Struct:
		return checkStruct(value, depth)
	}
	return nil
}

// checkMarshaled 处理有 MarshalJSON 的值：只有包装类型往里走
func checkMarshaled(value reflect.Value, depth int) error {
	if wrapped == nil || (value.Kind() == reflect.Pointer && value.IsNil()) {
		return nil
	}
	if inner, ok := wrapped(value.Interface()); ok {
		return checkValue(reflect.ValueOf(inner), depth+1)
	}
	return nil
}

// checkText 核对 MarshalText 的结果；nil 指针与 nil 接口写 null，与 encoding/json 相同
func checkText(value reflect.Value) error {
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	marshaler, ok := value.Interface().(encoding.TextMarshaler)
	if !ok {
		return nil
	}
	text, err := marshaler.MarshalText()
	if err != nil {
		return err
	}
	if !utf8.Valid(text) {
		return &invalidString{}
	}
	return nil
}

// checkItems 核对切片与数组的每一项
func checkItems(value reflect.Value, depth int) error {
	for index := range value.Len() {
		if err := checkValue(value.Index(index), depth+1); err != nil {
			return within(err, jsontext.IndexPath("", index))
		}
	}
	return nil
}

// checkMap 核对映射的键与值；键的文本与 encoding/json 相同（mapKey）
func checkMap(value reflect.Value, depth int) error {
	if value.IsNil() {
		return nil
	}
	iterator := value.MapRange()
	for iterator.Next() {
		name, err := mapKey(iterator.Key())
		if err != nil {
			return err
		}
		if !utf8.ValidString(name) {
			return &invalidString{member: true}
		}
		if err := checkValue(iterator.Value(), depth+1); err != nil {
			return within(err, jsontext.MemberPath("", name))
		}
	}
	return nil
}

// checkStruct 核对结构体写出的每个成员
func checkStruct(value reflect.Value, depth int) error {
	for _, member := range fieldsOf(value.Type()) {
		current, ok := fieldValue(value, member.index)
		if !ok || (member.omitZero && omittedAsZero(current)) {
			continue
		}
		if err := checkValue(current, depth+1); err != nil {
			return within(err, jsontext.MemberPath("", member.name))
		}
	}
	return nil
}

// omittedAsZero 是 encoding/json 的 omitzero 判定：字段的类型（或它的指针）有 IsZero() bool 时用它，nil 的指针与接口算零值，
// 否则看 reflect 的零值
func omittedAsZero(value reflect.Value) bool {
	t := value.Type()
	switch {
	case !value.CanInterface():
		return value.IsZero()
	case t.Kind() == reflect.Interface && t.Implements(zeroerType):
		return value.IsNil() || (value.Elem().Kind() == reflect.Pointer && value.Elem().IsNil()) || value.Interface().(interface{ IsZero() bool }).IsZero()
	case t.Kind() == reflect.Pointer && t.Implements(zeroerType):
		return value.IsNil() || value.Interface().(interface{ IsZero() bool }).IsZero()
	case t.Implements(zeroerType):
		return value.Interface().(interface{ IsZero() bool }).IsZero()
	case reflect.PointerTo(t).Implements(zeroerType):
		if !value.CanAddr() {
			// 与 encoding/json 相同：不可寻址的值先复制一份再取地址
			boxed := reflect.New(t).Elem()
			boxed.Set(value)
			value = boxed
		}
		return value.Addr().Interface().(interface{ IsZero() bool }).IsZero()
	}
	return value.IsZero()
}

// MarshalChecked 按值模型写出 value（Marshal），并要求写出的是合法的 JSON 文本（RFC-0001「写出与转交」，ADR-0320）：写出的整段文本
// 过共同检查（jsontext.Check：嵌套不超过 64 层、没有重复的成员名，json.Marshaler 原样交出的文本里没有非法的字节与孤立代理项的转义），
// 要写出的每个字符串都是合法的 UTF-8（CheckStrings；Marshal 把编成 UTF-8 的孤立代理项写成 \ud800 这样的转义、其余非法的字节写成
// U+FFFD，前者在整段文本上查得出，后者只能按值查）。层数从 value 的根算起，嵌在更外层里的由调用方对整段文本另做检查
func MarshalChecked(value any) ([]byte, error) {
	encoded, err := Marshal(value)
	if err != nil {
		return nil, err
	}
	if problem := jsontext.Check(encoded); problem != nil {
		return nil, fmt.Errorf("the written text does not pass the JSON text checks: %w", problem)
	}
	if err := CheckStrings(value); err != nil {
		return nil, err
	}
	return encoded, nil
}

// NotText 标出写不成合法 JSON 文本的原因（RFC-0001「写出与转交」，ADR-0320）：Result 与 Error 的线上编码以它作 FrameworkError 的 Cause；
// 操作的响应里嵌着它们时，调用器据此按写不成合法的 JSON 文本处理、不带 inner（RFC-0011 规则 10「编码」，ADR-0318）
type NotText struct {
	// Err 写明位置：共同检查的问题（*jsontext.Problem）或不是合法 UTF-8 的字符串
	Err error
}

// Error 实现 error。
func (e *NotText) Error() string { return e.Err.Error() }

// Unwrap 支持 errors.Is / errors.As。
func (e *NotText) Unwrap() error { return e.Err }
