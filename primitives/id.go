package primitives

import "encoding/json"

// ID 是类型化标识（RFC-0001 TypedId）：Tag 只用于区分类型，不承载数据；线上编码为底层值的编码。
type ID[Tag any, V comparable] struct {
	// Value 是底层值（string / int64 / UUID 字符串等）。
	Value V
}

// NewID 创建类型化标识。
func NewID[Tag any, V comparable](value V) ID[Tag, V] { return ID[Tag, V]{Value: value} }

// MarshalJSON 编码为底层值。
func (id ID[Tag, V]) MarshalJSON() ([]byte, error) { return json.Marshal(id.Value) }

// UnmarshalJSON 从底层值还原。
func (id *ID[Tag, V]) UnmarshalJSON(data []byte) error { return json.Unmarshal(data, &id.Value) }
