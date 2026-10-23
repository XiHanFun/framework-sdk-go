// Package primitives 是 XiHan Framework 原语层（RFC-0001）的 Go 形态：错误与错误码、Result、Maybe、
// 类型化标识、生命周期状态、管道阶段、稳定性、Baggage 键与消息原语。零行为、零依赖、只增不改；
// 语义与线上编码和 .NET 参考实现一致，由 framework-spec 的一致性向量验证。
package primitives

import (
	"fmt"
	"regexp"
	"strings"
)

// 原语层登记的错误码。
const (
	// ErrorCodeInvalidArgument：原语参数非法（空 Code、空消息键、清单跨字段约束等）。
	ErrorCodeInvalidArgument = "PRIMITIVES_VALIDATION_001"
	// ErrorCodeUninitializedResult：使用了未初始化的 Result（零值）。
	ErrorCodeUninitializedResult = "PRIMITIVES_VALIDATION_002"
)

// 错误码的 KIND 段（conventions/error-codes.md）；领域可以自定义其他 KIND。
const (
	KindValidation  = "VALIDATION"
	KindNotFound    = "NOTFOUND"
	KindConflict    = "CONFLICT"
	KindDenied      = "DENIED"
	KindUnavailable = "UNAVAILABLE"
	KindTimeout     = "TIMEOUT"
	KindCancelled   = "CANCELLED"
	KindUnexpected  = "UNEXPECTED"
)

// errorCodePattern 是错误码文法 <领域大写>_<KIND>_<NNN>。
var errorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*_[A-Z][A-Z0-9]*_[0-9]{3}$`)

// IsErrorCode 判断错误码是否符合 <领域大写>_<KIND>_<NNN>。
func IsErrorCode(code string) bool { return errorCodePattern.MatchString(code) }

// ValidateErrorCode 校验错误码并原样返回；不合法时返回 FrameworkError（PRIMITIVES_VALIDATION_001）。
func ValidateErrorCode(code string) (string, error) {
	if !IsErrorCode(code) {
		return "", &FrameworkError{Code: ErrorCodeInvalidArgument, Message: fmt.Sprintf("The error code '%s' must match <DOMAIN>_<KIND>_<NNN>.", code)}
	}
	return code, nil
}

// ErrorCodeKind 返回错误码的 KIND 段；不合法时为空。
func ErrorCodeKind(code string) string {
	if !IsErrorCode(code) {
		return ""
	}
	return strings.Split(code, "_")[1]
}

// ErrorCodeDomain 返回错误码的领域段；不合法时为空。
func ErrorCodeDomain(code string) string {
	if !IsErrorCode(code) {
		return ""
	}
	return strings.Split(code, "_")[0]
}
