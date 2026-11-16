package primitives

import (
	"strings"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// wireObject 读线上的 JSON 对象：先过共同的 JSON 读取检查（RFC-0001「JSON 文本」，ADR-0236）——语法按 RFC 8259、嵌套不超过 64 层、
// 字符串是合法的 Unicode、任何一层（含 value、details 与 inner 的内部）都没有重复的成员名，不取第一个也不取最后一个；
// 再要求根是对象（null 也不是）。成员按文本顺序读出，名字区分大小写
func wireObject(data []byte, what string) (*jsontext.Value, error) {
	root, problem := jsontext.Parse(data)
	if problem != nil {
		if problem.Syntax {
			return nil, wireError(what+" on the wire is not JSON", nil)
		}
		return nil, wireError(what+" on the wire has the member '"+problem.Path+"' more than once", nil)
	}
	if root.Kind != jsontext.Object {
		return nil, wireError(what+" on the wire must be a JSON object", nil)
	}
	return root, nil
}

// wireString 读线上的 JSON 字符串；成员没有或不是字符串（含 null）时第二个返回值为假
func wireString(value *jsontext.Value, has bool) (string, bool) {
	if !has || value.Kind != jsontext.String {
		return "", false
	}
	return value.Text, true
}

// raw 返回值在原文里的文本
func raw(data []byte, value *jsontext.Value) []byte { return data[value.Start:value.End] }

// wireError 创建线上编码不合法的配置错误（PRIMITIVES_VALIDATION_001）
func wireError(message string, cause error) error {
	return &FrameworkError{Code: ErrorCodeInvalidArgument, Message: strings.ToUpper(message[:1]) + message[1:] + ".", Cause: cause}
}
