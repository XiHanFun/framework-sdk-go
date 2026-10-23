package primitives

import "encoding/json"

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
		panic(&FrameworkError{Code: ErrorCodeInvalidArgument, Message: "The maybe has no value."})
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

// MarshalJSON 实现线上编码：有值为值本身，无值为 null。
func (m Maybe[T]) MarshalJSON() ([]byte, error) {
	if !m.has {
		return []byte("null"), nil
	}
	return json.Marshal(m.value)
}

// UnmarshalJSON 从线上编码还原：null 为无值。
func (m *Maybe[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*m = None[T]()
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*m = Some(value)
	return nil
}
