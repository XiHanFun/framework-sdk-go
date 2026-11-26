package primitives

import "github.com/xihanfun/framework-sdk-go/internal/jsoncodec"

// ID 是类型化标识（RFC-0001 TypedId）：Tag 只用于区分类型，不承载数据；线上编码为底层值的编码。
type ID[Tag any, V comparable] struct {
	// Value 是底层值（string / int64 / UUID 字符串等）。
	Value V
}

// NewID 创建类型化标识。
func NewID[Tag any, V comparable](value V) ID[Tag, V] { return ID[Tag, V]{Value: value} }

// MarshalJSON 编码为底层值。
func (id ID[Tag, V]) MarshalJSON() ([]byte, error) { return jsoncodec.Marshal(id.Value) }

// UnmarshalJSON 从底层值还原。
func (id *ID[Tag, V]) UnmarshalJSON(data []byte) error { return jsoncodec.Unmarshal(data, &id.Value) }

// jsonValue 是 MarshalJSON 写出的值：底层值
func (id ID[Tag, V]) jsonValue() any { return id.Value }

// wrapper 是 MarshalJSON 只把里面一个值交给 jsoncodec.Marshal 写出的类型（ID、Maybe）
type wrapper interface{ jsonValue() any }

// 让 jsoncodec.CheckStrings 往 ID 与 Maybe 里走（RFC-0011 规则 10「编码」，ADR-0318）：它们的 MarshalJSON 把非法的 UTF-8 写成 U+FFFD，
// 写出的文本里看不出来，要核对的是它们写出的值
func init() {
	jsoncodec.SetWrapped(func(value any) (any, bool) {
		if inner, ok := value.(wrapper); ok {
			return inner.jsonValue(), true
		}
		return nil, false
	})
}
