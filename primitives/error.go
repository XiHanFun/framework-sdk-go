package primitives

import (
	"encoding/json"
	"fmt"
)

// Error 是可恢复失败的描述（RFC-0001 Error）：错误码面向程序，消息键与参数面向人；不可变，方法返回新值。
type Error struct {
	// Code 是错误码 <领域大写>_<KIND>_<NNN>。
	Code string
	// MessageKey 是消息键；Literal 创建的为空。
	MessageKey string
	// Text 是已解析的文案；未解析为空。
	Text string
	// Details 是模板参数，也是结构化细节；只允许 JSON 可表示的值。
	Details map[string]any
	// Inner 是内部错误；可空。
	Inner *Error
}

// NewError 用错误码与消息键创建错误；码或键不合法是编程错误，panic FrameworkError。
func NewError(code, messageKey string, details map[string]any, inner *Error) *Error {
	mustErrorCode(code)
	if !IsMessageKey(messageKey) {
		panic(&FrameworkError{Code: ErrorCodeInvalidArgument, Message: fmt.Sprintf("The message key '%s' must match <domain>.<segment>[.<segment>].", messageKey)})
	}
	return &Error{Code: code, MessageKey: messageKey, Details: details, Inner: inner}
}

// Literal 用已解析的文案创建错误：线上还原与异常转换用，没有消息键。
func Literal(code, text string, details map[string]any, inner *Error) *Error {
	mustErrorCode(code)
	if text == "" {
		panic(&FrameworkError{Code: ErrorCodeInvalidArgument, Message: "A literal error needs a text."})
	}
	return &Error{Code: code, Text: text, Details: details, Inner: inner}
}

// Validation 创建 VALIDATION 类错误。
func Validation(code, messageKey string, details map[string]any) *Error {
	return ofKind(KindValidation, code, messageKey, details, nil)
}

// NotFound 创建 NOTFOUND 类错误。
func NotFound(code, messageKey string, details map[string]any) *Error {
	return ofKind(KindNotFound, code, messageKey, details, nil)
}

// Conflict 创建 CONFLICT 类错误。
func Conflict(code, messageKey string, details map[string]any) *Error {
	return ofKind(KindConflict, code, messageKey, details, nil)
}

// Denied 创建 DENIED 类错误。
func Denied(code, messageKey string, details map[string]any) *Error {
	return ofKind(KindDenied, code, messageKey, details, nil)
}

// Unavailable 创建 UNAVAILABLE 类错误。
func Unavailable(code, messageKey string, details map[string]any) *Error {
	return ofKind(KindUnavailable, code, messageKey, details, nil)
}

// Timeout 创建 TIMEOUT 类错误。
func Timeout(code, messageKey string, details map[string]any) *Error {
	return ofKind(KindTimeout, code, messageKey, details, nil)
}

// Cancelled 创建 CANCELLED 类错误。
func Cancelled(code, messageKey string, details map[string]any) *Error {
	return ofKind(KindCancelled, code, messageKey, details, nil)
}

// Unexpected 创建 UNEXPECTED 类错误，可带内部错误。
func Unexpected(code, messageKey string, inner *Error, details map[string]any) *Error {
	return ofKind(KindUnexpected, code, messageKey, details, inner)
}

// ofKind 创建错误并核对 KIND 段与工厂一致。
func ofKind(kind, code, messageKey string, details map[string]any, inner *Error) *Error {
	err := NewError(code, messageKey, details, inner)
	if err.Kind() != kind {
		panic(&FrameworkError{Code: ErrorCodeInvalidArgument, Message: fmt.Sprintf("The error code '%s' must have the kind %s.", code, kind)})
	}
	return err
}

// mustErrorCode 校验错误码，不合法即 panic。
func mustErrorCode(code string) {
	if _, err := ValidateErrorCode(code); err != nil {
		panic(err)
	}
}

// Kind 返回错误码的 KIND 段。
func (e *Error) Kind() string { return ErrorCodeKind(e.Code) }

// Domain 返回错误码的领域段。
func (e *Error) Domain() string { return ErrorCodeDomain(e.Code) }

// Message 返回面向人的消息：已解析文案；否则按进程默认区域解析消息键；再否则"键 (参数)"。
func (e *Error) Message() string {
	if e.Text != "" {
		return e.Text
	}
	if e.MessageKey == "" {
		return e.Code
	}
	if text, ok := ResolveMessage(e.MessageKey, e.Details); ok {
		return text
	}
	return Describe(e.MessageKey, e.Details)
}

// Error 实现 error：码 + 消息。
func (e *Error) Error() string { return e.Code + ": " + e.Message() }

// WithCode 换错误码，保留键、文案、参数与内部错误。
func (e *Error) WithCode(code string) *Error {
	mustErrorCode(code)
	clone := *e
	clone.Code = code
	return &clone
}

// WithText 填入已解析的文案。
func (e *Error) WithText(text string) *Error {
	clone := *e
	clone.Text = text
	return &clone
}

// Is 让 errors.Is 按错误码比较。
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.Code == e.Code
}

// errorJSON 是 Error 的线上编码（RFC-0001）。
type errorJSON struct {
	Code       string         `json:"code"`
	Message    string         `json:"message,omitempty"`
	MessageKey string         `json:"messageKey,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
	Inner      *Error         `json:"inner,omitempty"`
}

// MarshalJSON 实现线上编码：message 是发送端已解析的文案，messageKey 让接收端可再翻译。
func (e *Error) MarshalJSON() ([]byte, error) {
	return json.Marshal(errorJSON{Code: e.Code, Message: e.Message(), MessageKey: e.MessageKey, Details: e.Details, Inner: e.Inner})
}

// UnmarshalJSON 从线上编码还原：码不合法时整体失败。
func (e *Error) UnmarshalJSON(data []byte) error {
	var wire errorJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if !IsErrorCode(wire.Code) {
		return &FrameworkError{Code: ErrorCodeInvalidArgument, Message: fmt.Sprintf("The error code '%s' must match <DOMAIN>_<KIND>_<NNN>.", wire.Code)}
	}
	*e = Error{Code: wire.Code, MessageKey: wire.MessageKey, Text: wire.Message, Details: wire.Details, Inner: wire.Inner}
	return nil
}
