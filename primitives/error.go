package primitives

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/xihanfun/framework-sdk-go/internal/jsoncodec"
	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
	"github.com/xihanfun/framework-spec/conformance/rules"
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
		panic(NewFrameworkError(ErrorCodeInvalidArgument, "The message key '%s' must match <domain>.<segment>[.<segment>].", messageKey))
	}
	return &Error{Code: code, MessageKey: messageKey, Details: details, Inner: inner}
}

// Literal 用已解析的文案创建错误：线上还原与异常转换用，没有消息键；文案为空或只有空白是编程错误，panic FrameworkError。
func Literal(code, text string, details map[string]any, inner *Error) *Error {
	mustErrorCode(code)
	mustText(text, "A literal error")
	return &Error{Code: code, Text: text, Details: details, Inner: inner}
}

// isBlank 判断文案是否为空或只有空白：空白按 RFC-0001「空白字符」的 25 个码位（rules.IsWhitespace，与握手行同一个函数），
// 不用 unicode.IsSpace，免得与规范的表悄悄分叉（ADR-0245）。
func isBlank(text string) bool { return strings.TrimFunc(text, rules.IsWhitespace) == "" }

// mustText 校验自由文本：为空或只有空白即 panic。
func mustText(text, owner string) {
	if isBlank(text) {
		panic(NewFrameworkError(ErrorCodeInvalidArgument, "%s needs a text that is not empty or whitespace.", owner))
	}
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

// Unexpected 创建 UNEXPECTED 类错误，可带内部错误；参数顺序与其他工厂一致，先细节、后内部错误（RFC-0001、ADR-0137）
func Unexpected(code, messageKey string, details map[string]any, inner *Error) *Error {
	return ofKind(KindUnexpected, code, messageKey, details, inner)
}

// ofKind 创建错误并核对 KIND 段与工厂一致。
func ofKind(kind, code, messageKey string, details map[string]any, inner *Error) *Error {
	err := NewError(code, messageKey, details, inner)
	if err.Kind() != kind {
		panic(NewFrameworkError(ErrorCodeInvalidArgument, "The error code '%s' must have the kind %s.", code, kind))
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

// Message 返回面向人的消息：已解析文案；否则按进程默认区域解析消息键，解析不出时用登记的 en-US 默认模板；再否则"键 (参数)"
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

// WithText 填入已解析的文案；文案为空或只有空白是编程错误，panic FrameworkError。
func (e *Error) WithText(text string) *Error {
	mustText(text, "WithText")
	clone := *e
	clone.Text = text
	return &clone
}

// Is 让 errors.Is 按错误码比较。
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.Code == e.Code
}

// Unwrap 返回内部错误，让 errors.Is / errors.As 沿着 Inner 往里找；没有内部错误时返回 nil。
func (e *Error) Unwrap() error {
	// 不能直接 return e.Inner：nil 的 *Error 装进 error 接口不等于 nil，errors.Is / errors.As 会接着在它上面调方法
	if e.Inner == nil {
		return nil
	}
	return e.Inner
}

// MarshalJSON 实现线上编码（RFC-0001）：成员按 code、message、messageKey、details、inner 的顺序写出；message 是发送端已解析的文案，
// 总是写出；messageKey 只在有键时写（Literal 没有键），让接收端可再翻译；details 为空、inner 没有时不写。细节按值模型写出：
// 整数值的浮点带小数点，非有限值写成字符串，64 位范围内的整数原样（ADR-0236）。
//
// 写不成合法的 JSON 文本即写不出（RFC-0001「写出与转交」，ADR-0320），返回 FrameworkError（PRIMITIVES_VALIDATION_001）、不交出文本：
// 字符串（文案、细节的成员名与值，含内部错误的）不是合法的 UTF-8（含编成 UTF-8 的孤立代理项），不写成 U+FFFD 或 \ud800 这样的转义；
// 写出的整段文本没过共同检查（从错误的根算起嵌套超过 64 层，细节里原样嵌进去的文本有重复的成员名或孤立代理项的转义）
func (e *Error) MarshalJSON() ([]byte, error) {
	encoded, err := e.appendWire(nil)
	if err != nil {
		return nil, err
	}
	return checkWireText(encoded, "the error")
}

// appendWire 把错误的线上编码接在 dst 后面：逐个核对要写出的字符串，整段文本的共同检查由写出的根（MarshalJSON 或 Result）做，
// 嵌在结果里的错误层数要从结果的根算起
func (e *Error) appendWire(dst []byte) ([]byte, error) {
	message := e.Message()
	for _, member := range [...]struct{ name, text string }{{"code", e.Code}, {"message", message}, {"messageKey", e.MessageKey}} {
		if !utf8.ValidString(member.text) {
			return nil, notText("the error", errors.New("the member '"+member.name+"' is not valid UTF-8"))
		}
	}
	encoded := jsontext.AppendString(append(dst, `{"code":`...), e.Code)
	encoded = append(encoded, `,"message":`...)
	encoded = jsontext.AppendString(encoded, message)
	if e.MessageKey != "" {
		encoded = append(encoded, `,"messageKey":`...)
		encoded = jsontext.AppendString(encoded, e.MessageKey)
	}
	if len(e.Details) > 0 {
		details, err := jsoncodec.Marshal(e.Details)
		if err != nil {
			return nil, err
		}
		// jsoncodec.Marshal 把非法的字节写成 U+FFFD，写出的文本里看不出来，按值再查一遍
		if err := jsoncodec.CheckStrings(e.Details); err != nil {
			return nil, notText("the error", fmt.Errorf("the details: %w", err))
		}
		encoded = append(append(encoded, `,"details":`...), details...)
	}
	if e.Inner != nil {
		var err error
		if encoded, err = e.Inner.appendWire(append(encoded, `,"inner":`...)); err != nil {
			return nil, err
		}
	}
	return append(encoded, '}'), nil
}

// checkWireText 对写出的整段线上编码做共同检查（RFC-0001「JSON 文本」）：嵌套不超过 64 层、没有重复的成员名、没有孤立代理项的转义与
// 非法的字节；没过即写不出，返回 FrameworkError（PRIMITIVES_VALIDATION_001）
func checkWireText(encoded []byte, what string) ([]byte, error) {
	if problem := jsontext.Check(encoded); problem != nil {
		return nil, notText(what, problem)
	}
	return encoded, nil
}

// notText 创建线上编码写不成合法 JSON 文本的配置错误（PRIMITIVES_VALIDATION_001）：Cause 是标出这类原因的 jsoncodec.NotText
func notText(what string, cause error) error {
	return wireError(what+" cannot be written as JSON text", &jsoncodec.NotText{Err: cause})
}

// UnmarshalJSON 从线上编码还原（RFC-0001）：先过共同的 JSON 读取检查；code 必填且合法；message 必填、是字符串，不能为空或只有空白；messageKey 有就必须合法；
// details 必须是 JSON 对象，inner 必须是错误对象（null 与数组都拒绝）；成员名区分大小写，未知成员忽略
func (e *Error) UnmarshalJSON(data []byte) error {
	root, err := wireObject(data, "an error")
	if err != nil {
		return err
	}
	decoded, err := decodeError(data, root)
	if err != nil {
		return err
	}
	*e = *decoded
	return nil
}

// decodeError 从已过共同检查的文本里读一个错误对象，inner 递归
func decodeError(data []byte, root *jsontext.Value) (*Error, error) {
	if root.Kind != jsontext.Object {
		return nil, wireError("an error on the wire must be a JSON object", nil)
	}
	codeValue, has := root.Find("code")
	if !has {
		return nil, wireError("an error on the wire needs the member 'code'", nil)
	}
	code, ok := wireString(codeValue, true)
	if !ok || !IsErrorCode(code) {
		return nil, NewFrameworkError(ErrorCodeInvalidArgument, "The error code %s must match <DOMAIN>_<KIND>_<NNN>.", raw(data, codeValue))
	}
	decoded := &Error{Code: code}
	// 只有空白的 message 与空串一样不合法（RFC-0001「空白字符」，ADR-0245）：Error 的文案至少要有一个空白之外的字符
	if decoded.Text, ok = wireString(root.Find("message")); !ok || isBlank(decoded.Text) {
		return nil, wireError("an error on the wire needs the member 'message' as a string that is not empty or whitespace", nil)
	}
	if key, has := root.Find("messageKey"); has {
		if decoded.MessageKey, ok = wireString(key, true); !ok || !IsMessageKey(decoded.MessageKey) {
			return nil, NewFrameworkError(ErrorCodeInvalidArgument, "The message key %s of an error on the wire must match <domain>.<segment>[.<segment>].", raw(data, key))
		}
	}
	if details, has := root.Find("details"); has {
		if details.Kind != jsontext.Object {
			return nil, wireError("the member 'details' of an error must be a JSON object", nil)
		}
		// 细节按值模型读：64 位范围内整数写法的数字是 int64，其余是 float64（ADR-0236）
		decoded.Details = jsoncodec.FromJSON(details).(map[string]any)
	}
	if inner, has := root.Find("inner"); has {
		nested, err := decodeError(data, inner)
		if err != nil {
			return nil, err
		}
		decoded.Inner = nested
	}
	return decoded, nil
}
