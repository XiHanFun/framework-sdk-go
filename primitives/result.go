package primitives

import (
	"fmt"

	"github.com/xihanfun/framework-sdk-go/internal/jsoncodec"
	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// Unit 是没有值的成功结果的类型参数。
type Unit struct{}

// Result 是成功或失败（RFC-0001 Result / Result<T>）：成功带值，失败带 Error，二者互斥；零值视为未初始化的失败。
type Result[T any] struct {
	value T
	err   *Error
	ok    bool
}

// Ok 创建成功结果。
func Ok[T any](value T) Result[T] { return Result[T]{value: value, ok: true} }

// Fail 创建失败结果；err 为 nil 是编程错误。
func Fail[T any](err *Error) Result[T] {
	if err == nil {
		panic(NewFrameworkError(ErrorCodeInvalidArgument, "A failed result needs an error."))
	}
	return Result[T]{err: err}
}

// Success 创建没有值的成功结果。
func Success() Result[Unit] { return Ok(Unit{}) }

// OK 判断是否成功。
func (r Result[T]) OK() bool { return r.ok }

// Failed 判断是否失败；未初始化也算失败。
func (r Result[T]) Failed() bool { return !r.ok }

// Err 返回失败原因；成功时为 nil，未初始化时为 PRIMITIVES_VALIDATION_002（primitives.result.uninitialized）
func (r Result[T]) Err() *Error {
	if r.ok {
		return nil
	}
	if r.err == nil {
		return Validation(ErrorCodeUninitializedResult, MessageUninitializedResult, nil)
	}
	return r.err
}

// Value 返回成功值；失败时第二个返回值为假。
func (r Result[T]) Value() (T, bool) { return r.value, r.ok }

// Or 返回成功值，失败时返回备选值。
func (r Result[T]) Or(fallback T) T {
	if r.ok {
		return r.value
	}
	return fallback
}

// Must 返回成功值；失败时 panic FrameworkError（码为错误的码）。
func (r Result[T]) Must() T {
	if !r.ok {
		err := r.Err()
		panic(NewFrameworkError(err.Code, "%s", err.Message()))
	}
	return r.value
}

// Unit 丢掉值，只保留成功与否。
func (r Result[T]) Unit() Result[Unit] {
	if r.ok {
		return Success()
	}
	return Fail[Unit](r.Err())
}

// String 便于日志。
func (r Result[T]) String() string {
	if r.ok {
		return fmt.Sprintf("Ok(%v)", r.value)
	}
	return "Fail(" + r.Err().Error() + ")"
}

// Map 把成功值变换成另一种类型；失败原样传递。
func Map[T, U any](r Result[T], f func(T) U) Result[U] {
	if !r.ok {
		return Fail[U](r.Err())
	}
	return Ok(f(r.value))
}

// Bind 把成功值交给返回 Result 的函数；失败原样传递。
func Bind[T, U any](r Result[T], f func(T) Result[U]) Result[U] {
	if !r.ok {
		return Fail[U](r.Err())
	}
	return f(r.value)
}

// maybeValue 由 Maybe 实现：成功结果的值只有是 Maybe 时才可以是 null（RFC-0001）
type maybeValue interface{ isMaybe() }

// isUnit 判断 T 是否是 Unit：没有值的成功结果线上不写 value
func isUnit[T any]() bool {
	_, ok := any(*new(T)).(Unit)
	return ok
}

// isMaybe 判断 T 是否是 Maybe
func isMaybe[T any]() bool {
	_, ok := any(*new(T)).(maybeValue)
	return ok
}

// MarshalJSON 实现线上编码（RFC-0001）：有值的成功 {"ok":true,"value":…}，没有值的成功（Result[Unit]）{"ok":true}，
// 失败 {"ok":false,"error":{…}}；值编码成 null 而值类型不是 Maybe 时报错，可能没有的值用 Maybe。
//
// 写不成合法的 JSON 文本即写不出（RFC-0001「写出与转交」，ADR-0320），返回 FrameworkError（PRIMITIVES_VALIDATION_001）、不交出文本：
// 成功值与错误里的字符串（成员名与值，含映射的键、ID 与 Maybe 里的）不是合法的 UTF-8（含编成 UTF-8 的孤立代理项），
// 不写成 U+FFFD 或 \ud800 这样的转义；写出的整段文本没过共同检查：嵌套从结果的根算起（成功值的最外层是第 2 层）超过 64 层，
// 或原样嵌进去的文本（json.Marshaler、json.RawMessage）有重复的成员名、孤立代理项的转义或非法的字节
func (r Result[T]) MarshalJSON() ([]byte, error) {
	if !r.ok {
		encoded, err := r.Err().appendWire([]byte(`{"ok":false,"error":`))
		if err != nil {
			return nil, err
		}
		return checkWireText(append(encoded, '}'), "the result")
	}
	if isUnit[T]() {
		return []byte(`{"ok":true}`), nil
	}
	// 值按值模型写出：整数值的浮点带小数点，非有限值写成字符串（RFC-0001「Result」，ADR-0236）
	value, err := jsoncodec.Marshal(r.value)
	if err != nil {
		return nil, err
	}
	if string(value) == "null" && !isMaybe[T]() {
		return nil, wireError("a successful result needs a value; a value that may be absent must be a Maybe", nil)
	}
	encoded, err := checkWireText(append(append([]byte(`{"ok":true,"value":`), value...), '}'), "the result")
	if err != nil {
		return nil, err
	}
	// jsoncodec.Marshal 把非法的字节写成 U+FFFD，写出的文本里看不出来：层数核对过了，按值再走一遍
	if err := jsoncodec.CheckStrings(r.value); err != nil {
		return nil, notText("the result", fmt.Errorf("the value: %w", err))
	}
	return encoded, nil
}

// UnmarshalJSON 从线上编码还原（RFC-0001）：ok 必须是布尔值；成功不能带 error，有值的成功必须带 value（只有 Maybe 可以是 null），
// 没有值的成功不能带 value；失败必须带 error 对象、不能带 value；成员名区分大小写，未知成员忽略
func (r *Result[T]) UnmarshalJSON(data []byte) error {
	root, err := wireObject(data, "a result")
	if err != nil {
		return err
	}
	okValue, has := root.Find("ok")
	if !has {
		return wireError("a result on the wire needs the member 'ok'", nil)
	}
	if okValue.Kind != jsontext.Bool {
		return wireError("the member 'ok' of a result must be a boolean", nil)
	}
	value, hasValue := root.Find("value")
	failure, hasError := root.Find("error")
	if !okValue.Bool {
		if hasValue {
			return wireError("a failed result on the wire must not carry a value", nil)
		}
		if !hasError {
			return wireError("a failed result on the wire needs an error", nil)
		}
		decoded, err := decodeError(data, failure)
		if err != nil {
			return err
		}
		*r = Fail[T](decoded)
		return nil
	}
	if hasError {
		return wireError("a successful result on the wire must not carry an error", nil)
	}
	if isUnit[T]() {
		if hasValue {
			return wireError("a successful result without a value must not carry the member 'value'", nil)
		}
		*r = Ok(*new(T))
		return nil
	}
	if !hasValue {
		return wireError("a successful result on the wire needs the member 'value'", nil)
	}
	if value.Kind == jsontext.Null && !isMaybe[T]() {
		return wireError("the value of a successful result must not be null unless it is a Maybe", nil)
	}
	var decoded T
	if problem := jsoncodec.Decode(data, value, &decoded); problem != nil {
		return wireError("the value of a successful result does not match its type at '"+problem.Path+"'", problem.Cause)
	}
	*r = Ok(decoded)
	return nil
}
