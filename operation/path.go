package operation

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
)

// bracketedNameCharacters 是成员名里要写成 ['名字'] 的字符（RFC-0011 规则 2，照 System.Text.Json 的实测）：
// . 空格 ' / " [ ] ( ) \ 制表 换行 回车 换页 退格 U+0085 U+2028 U+2029；其余字符照写，名字原样、不转义
const bracketedNameCharacters = ". '/\"[]()\\\t\n\r\f\b\u0085  "

// payloadPath 把载荷的解码错误换成出错位置的 JSON 路径（RFC-0011 规则 2，ADR-0124、ADR-0235）：只写位置、不带目标类型名。
// 类型不匹配按错误的偏移在载荷里定位，写成 $.quantity、$.lines[1].quantity、$['unit price'] 这样的路径；根上就不合时写 $。
// 不是整数、浮点或布尔能接受的 null（encoding/json 把它悄悄当成零值）也算类型不符，与解码错误取载荷里靠前的那个。
// 语法错误与其他错误写 $（规范只要求以 $ 开头）
func payloadPath(payload []byte, target reflect.Type, err error) (string, bool) {
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return "$", true
	}
	nullAt, nullOffset, hasNull := findNull(payload, target)
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		if hasNull && nullOffset <= typeError.Offset {
			return nullAt, true
		}
		if path, ok := locate(payload, typeError.Offset); ok {
			return path, true
		}
		return "$", true
	}
	if err != nil {
		return "$", true
	}
	return nullAt, hasNull
}

// pathFrame 是定位时的一层容器：数组记下一个元素的下标，对象记当前成员名
type pathFrame struct {
	array     bool
	index     int
	key       string
	wantValue bool
}

// locate 逐个记号读载荷，返回第一个结束位置不早于 offset 的值的路径：标量的偏移是它的结束位置，容器的偏移紧跟在左括号之后
func locate(payload []byte, offset int64) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var stack []*pathFrame
	completed := func() {
		if len(stack) == 0 {
			return
		}
		top := stack[len(stack)-1]
		if top.array {
			top.index++
		} else {
			top.wantValue = false
		}
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		delim, isDelim := token.(json.Delim)
		if isDelim && (delim == '}' || delim == ']') {
			stack = stack[:len(stack)-1]
			completed()
			continue
		}
		if len(stack) > 0 && !stack[len(stack)-1].array && !stack[len(stack)-1].wantValue {
			stack[len(stack)-1].key, _ = token.(string)
			stack[len(stack)-1].wantValue = true
			continue
		}
		if decoder.InputOffset() >= offset {
			return pathOf(stack), true
		}
		if isDelim {
			stack = append(stack, &pathFrame{array: delim == '['})
			continue
		}
		completed()
	}
}

// pathOf 写路径：$ 后接各层的 [下标] 或 .成员名
func pathOf(stack []*pathFrame) string {
	path := "$"
	for _, frame := range stack {
		if frame.array {
			path = indexPath(path, frame.index)
		} else {
			path = memberPath(path, frame.key)
		}
	}
	return path
}

// memberPath 在路径后接一个成员：含 bracketedNameCharacters 里的字符时写 ['名字']，否则写 .名字（空名字写成 .）
func memberPath(path, name string) string {
	if strings.ContainsAny(name, bracketedNameCharacters) {
		return path + "['" + name + "']"
	}
	return path + "." + name
}

// indexPath 在路径后接一个数组下标
func indexPath(path string, index int) string { return path + "[" + strconv.Itoa(index) + "]" }

// findNull 按请求类型走一遍载荷，找第一个落在整数、浮点或布尔上的 null：encoding/json 把这样的 null 当成零值、不报错，
// 而 .NET 的值类型不接受 null。字符串、结构体、切片、映射、指针与接口接受 null（与 .NET 的引用类型一样）；
// 自己实现 UnmarshalJSON / UnmarshalText 的类型由它自己决定，不往里看。返回路径与 null 结束处的偏移
func findNull(payload []byte, target reflect.Type) (string, int64, bool) {
	walker := &nullWalker{decoder: json.NewDecoder(bytes.NewReader(payload))}
	path, found := walker.value(target, "$")
	return path, walker.offset, found && walker.err == nil
}

// nullWalker 是 findNull 的递归下降读取器
type nullWalker struct {
	decoder *json.Decoder
	offset  int64
	err     error
}

var (
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// value 读一个值；target 为 nil 时只读不查
func (w *nullWalker) value(target reflect.Type, path string) (string, bool) {
	token, err := w.decoder.Token()
	if err != nil {
		w.err = err
		return "", false
	}
	target = inspectable(target)
	if token == nil {
		if target != nil && rejectsNull(target) {
			w.offset = w.decoder.InputOffset()
			return path, true
		}
		return "", false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return "", false
	}
	if delim == '[' {
		var element reflect.Type
		if target != nil && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array) {
			element = target.Elem()
		}
		for index := 0; w.decoder.More(); index++ {
			if found, ok := w.value(element, indexPath(path, index)); ok || w.err != nil {
				return found, ok
			}
		}
		_, w.err = w.decoder.Token()
		return "", false
	}
	for w.decoder.More() {
		key, err := w.decoder.Token()
		if err != nil {
			w.err = err
			return "", false
		}
		name, _ := key.(string)
		if found, ok := w.value(memberType(target, name), memberPath(path, name)); ok || w.err != nil {
			return found, ok
		}
	}
	_, w.err = w.decoder.Token()
	return "", false
}

// inspectable 去掉指针，自己实现解码的类型与接口返回 nil（不往里看）
func inspectable(target reflect.Type) reflect.Type {
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == nil || target.Kind() == reflect.Interface {
		return nil
	}
	pointer := reflect.PointerTo(target)
	if target.Implements(jsonUnmarshalerType) || pointer.Implements(jsonUnmarshalerType) || target.Implements(textUnmarshalerType) || pointer.Implements(textUnmarshalerType) {
		return nil
	}
	return target
}

// rejectsNull 判断类型是否不接受 null：整数、浮点与布尔
func rejectsNull(target reflect.Type) bool {
	switch target.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// memberType 返回对象成员的类型：映射取值类型；结构体按 encoding/json 的匹配取字段（先按名字精确匹配，再不区分大小写）
func memberType(target reflect.Type, name string) reflect.Type {
	if target == nil {
		return nil
	}
	switch target.Kind() {
	case reflect.Map:
		return target.Elem()
	case reflect.Struct:
		var folded reflect.Type
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if !field.IsExported() || tag == "-" {
				continue
			}
			if tag == "" {
				tag = field.Name
			}
			if tag == name {
				return field.Type
			}
			if folded == nil && strings.EqualFold(tag, name) {
				folded = field.Type
			}
		}
		return folded
	}
	return nil
}
