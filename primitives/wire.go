package primitives

import (
	"encoding/json"
	"strings"
)

// wireObject 把线上的 JSON 对象读成成员表：成员名区分大小写；不是对象（含 null）即失败
func wireObject(data []byte, what string) (map[string]json.RawMessage, error) {
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
