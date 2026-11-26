package primitives

import (
	"fmt"

	"github.com/xihanfun/framework-sdk-go/internal/jsoncodec"
)

// Maybe 是可能没有的值（RFC-0001 Maybe<T>）；零值即无值。线上编码：有值为值本身，无值为 null。
type Maybe[T any] struct {
	value T
	has   bool
}

// Some 创建有值的 Maybe。
func Some[T any](value T) Maybe[T] { return Maybe[T]{value: value, has: true} }

// None 创建无值的 Maybe。
func None[T any]() Maybe[T] { return Maybe[T]{} }

// FromPointer 把指针转成 Maybe：nil 为无值。
func FromPointer[T any](value *T) Maybe[T] {
	if value == nil {
		return None[T]()
	}
	return Some(*value)
}

// HasValue 判断是否有值。
func (m Maybe[T]) HasValue() bool { return m.has }

// Value 返回值；无值时第二个返回值为假。
func (m Maybe[T]) Value() (T, bool) { return m.value, m.has }

// Or 返回值，无值时返回备选值。
func (m Maybe[T]) Or(fallback T) T {
	if m.has {
		return m.value
	}
	return fallback
}

// Must 返回值；无值时 panic FrameworkError。
func (m Maybe[T]) Must() T {
	if !m.has {
		panic(NewFrameworkError(ErrorCodeInvalidArgument, "The maybe has no value."))
	}
	return m.value
}

// ToResult 把无值转成给定错误的失败。
func (m Maybe[T]) ToResult(err *Error) Result[T] {
	if m.has {
		return Ok(m.value)
	}
	return Fail[T](err)
}

// MapMaybe 变换有值的 Maybe；无值原样传递。
func MapMaybe[T, U any](m Maybe[T], f func(T) U) Maybe[U] {
	if !m.has {
		return None[U]()
	}
	return Some(f(m.value))
}

// BindMaybe 把有值的 Maybe 交给返回 Maybe 的函数（RFC-0001 Maybe<T> 的 Bind）；无值原样传递，不调函数。
func BindMaybe[T, U any](m Maybe[T], f func(T) Maybe[U]) Maybe[U] {
	if !m.has {
		return None[U]()
	}
	return f(m.value)
}

// String 便于日志：有值为 Some(值)，无值为 None，与 .NET 的 Maybe<T>.ToString 相同。
func (m Maybe[T]) String() string {
	if !m.has {
		return "None"
	}
	return fmt.Sprintf("Some(%v)", m.value)
}

// isMaybe 让 Result 认出 Maybe：成功值只有是 Maybe 时才可以是 null
func (Maybe[T]) isMaybe() {}

// MarshalJSON 实现线上编码：有值为值本身，无值为 null。
func (m Maybe[T]) MarshalJSON() ([]byte, error) {
	if !m.has {
		return []byte("null"), nil
	}
	return jsoncodec.Marshal(m.value)
}

// jsonValue 是 MarshalJSON 写出的值：有值为值本身，无值为 nil（写 null）
func (m Maybe[T]) jsonValue() any {
	if !m.has {
		return nil
	}
	return m.value
}

// UnmarshalJSON 从线上编码还原：null 为无值。
func (m *Maybe[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*m = None[T]()
		return nil
	}
	var value T
	if err := jsoncodec.Unmarshal(data, &value); err != nil {
		return err
	}
	*m = Some(value)
	return nil
}
