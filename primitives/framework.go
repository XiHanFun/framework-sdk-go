package primitives

import "fmt"

// 框架信息（RFC-0001 FrameworkInfo）。
const (
	// Name 是框架名。
	Name = "XiHan Framework"
	// SpecVersion 是本 SDK 实现的原语层大版本。
	SpecVersion = "1"
	// SDKName 是握手时上报的 SDK 名。
	SDKName = "framework-sdk-go"
	// SDKVersion 是本 SDK 的版本。
	SDKVersion = "0.2.0-rc.1"
	// Repository 是 SDK 仓库地址。
	Repository = "https://github.com/XiHanFun/framework-sdk-go"
	// Documentation 是文档地址。
	Documentation = "https://framework.docs.xihanfun.com"
)

// FrameworkError 是配置或编程错误（RFC-0001 FrameworkException）：带错误码，消息是英文、不本地化；
// 可恢复的业务失败用 Error 与 Result，不用它。
type FrameworkError struct {
	// Code 是错误码。
	Code string
	// Message 是面向开发者的英文说明。
	Message string
	// Cause 是底层错误；可空。
	Cause error
}

// NewFrameworkError 创建配置或编程错误：code 是错误码，消息由 format 与 arguments 按 fmt.Sprintf 组装（英文，句子以句号结尾，
// 标识符用单引号）。要带底层错误的在返回值上设 Cause
func NewFrameworkError(code, format string, arguments ...any) *FrameworkError {
	return &FrameworkError{Code: code, Message: fmt.Sprintf(format, arguments...)}
}

// Error 实现 error。
func (e *FrameworkError) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Message + " (" + e.Cause.Error() + ")"
	}
	return e.Code + ": " + e.Message
}

// Unwrap 支持 errors.Is / errors.As。
func (e *FrameworkError) Unwrap() error { return e.Cause }

// ToError 转成可上线的 Error（Literal，带已解析文案）。
func (e *FrameworkError) ToError() *Error {
	code := e.Code
	if !IsErrorCode(code) {
		code = ErrorCodeInvalidArgument
	}
	return Literal(code, e.Message, nil, nil)
}
