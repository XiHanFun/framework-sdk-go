package primitives

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xihanfun/framework-sdk-go/internal/jsonstrict"
)

// wireObject 把线上的 JSON 对象读成成员表：成员名区分大小写；不是对象（含 null）即失败；同一层出现重复的成员名不合法，
// 不取第一个也不取最后一个（ADR-0235），对象里嵌套的值（value、details、inner）一并核对
func wireObject(data []byte, what string) (map[string]json.RawMessage, error) {
	if name, found, _ := jsonstrict.DuplicateMember(data); found {
		return nil, wireError(fmt.Sprintf("%s on the wire has the member '%s' more than once in the same object", what, name), nil)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil || members == nil {
		return nil, wireError(what+" on the wire must be a JSON object", err)
	}
	return members, nil
}

// wireString 读线上的 JSON 字符串；不是字符串（含 null）时第二个返回值为假
func wireString(raw json.RawMessage) (string, bool) {
	var text string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

// wireError 创建线上编码不合法的配置错误（PRIMITIVES_VALIDATION_001）
func wireError(message string, cause error) error {
	return &FrameworkError{Code: ErrorCodeInvalidArgument, Message: strings.ToUpper(message[:1]) + message[1:] + ".", Cause: cause}
}
