package primitives

import (
	"encoding/json"
	"fmt"
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
		panic(&FrameworkError{Code: ErrorCodeInvalidArgument, Message: "A failed result needs an error."})
	}
	return Result[T]{err: err}
}

// Success 创建没有值的成功结果。
func Success() Result[Unit] { return Ok(Unit{}) }

// OK 判断是否成功。
func (r Result[T]) OK() bool { return r.ok }

// Failed 判断是否失败；未初始化也算失败。
func (r Result[T]) Failed() bool { return !r.ok }

// Err 返回失败原因；成功时为 nil，未初始化时为 PRIMITIVES_VALIDATION_002。
func (r Result[T]) Err() *Error {
	if r.ok {
		return nil
	}
	if r.err == nil {
		return Literal(ErrorCodeUninitializedResult, "The result has not been initialized.", nil, nil)
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
		panic(&FrameworkError{Code: err.Code, Message: err.Message()})
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

// resultJSON 是 Result 的线上编码（RFC-0001）。
type resultJSON[T any] struct {
	OK    bool   `json:"ok"`
	Value *T     `json:"value,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// MarshalJSON 实现线上编码：{ "ok": true, "value": … } 或 { "ok": false, "error": … }。
func (r Result[T]) MarshalJSON() ([]byte, error) {
	if r.ok {
		return json.Marshal(resultJSON[T]{OK: true, Value: &r.value})
	}
	return json.Marshal(resultJSON[T]{OK: false, Error: r.Err()})
}

// UnmarshalJSON 从线上编码还原。
func (r *Result[T]) UnmarshalJSON(data []byte) error {
	var wire resultJSON[T]
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.OK {
		var value T
		if wire.Value != nil {
			value = *wire.Value
		}
		*r = Ok(value)
		return nil
	}
	if wire.Error == nil {
		return &FrameworkError{Code: ErrorCodeInvalidArgument, Message: "A failed result on the wire needs an error."}
	}
	*r = Fail[T](wire.Error)
	return nil
}
