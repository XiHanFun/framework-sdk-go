package operation

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// payloadPath 把载荷的解码错误换成出错位置的 JSON 路径（RFC-0011 规则 2、ADR-0124）：只写位置、不带目标类型名，
// 类型不匹配按错误的偏移在载荷里定位，写成 $.quantity、$.lines[0].quantity 这样的路径（与 .NET System.Text.Json 的
// JsonException.Path 同一写法）；语法错误与其他错误写 $
func payloadPath(payload []byte, err error) string {
	var typeError *json.UnmarshalTypeError
	if !errors.As(err, &typeError) {
		return "$"
	}
	if path, ok := locate(payload, typeError.Offset); ok {
		return path
	}
	return "$"
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

// pathOf 写路径：$ 后接各层的 [下标] 或 .成员名；成员名含特殊字符时写 ['成员名']
func pathOf(stack []*pathFrame) string {
	var builder strings.Builder
	builder.WriteByte('$')
	for _, frame := range stack {
		if frame.array {
			builder.WriteByte('[')
			builder.WriteString(strconv.Itoa(frame.index))
			builder.WriteByte(']')
			continue
		}
		if strings.ContainsAny(frame.key, ". '/\"[]()\t\n\r\f\b\\\u0085  ") {
			builder.WriteString("['")
			builder.WriteString(strings.ReplaceAll(frame.key, "'", `\'`))
			builder.WriteString("']")
			continue
		}
		builder.WriteByte('.')
		builder.WriteString(frame.key)
	}
	return builder.String()
}
