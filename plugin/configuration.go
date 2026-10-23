package plugin

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
)

// Decode 把配置节解码到 target（指向结构体的指针）。宿主的配置源是键值字符串（.NET 的 IConfiguration），
// 数字与布尔都以 JSON 字符串下发（lifecycle.md §2），所以先按 target 的字段类型把字符串叶子转成数字或布尔，再交给 encoding/json；
// 本来就是数字或布尔的 JSON 原样接受。空配置节解码为零值。
func (c Configuration) Decode(target any) error {
	if target == nil {
		return errors.New("plugin: the decode target must be a non-nil pointer")
	}
	pointer := reflect.ValueOf(target)
	if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return errors.New("plugin: the decode target must be a non-nil pointer")
	}
	if len(c.Section) == 0 {
		return nil
	}
	var raw any
	if err := json.Unmarshal(c.Section, &raw); err != nil {
		return err
	}
	coerced, err := json.Marshal(coerce(raw, pointer.Type().Elem()))
	if err != nil {
		return err
	}
	return json.Unmarshal(coerced, target)
}

// coerce 按目标类型递归地把字符串叶子转成数字或布尔；转不了的原样保留，让 encoding/json 报它自己的错误。
func coerce(value any, target reflect.Type) any {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch typed := value.(type) {
	case map[string]any:
		switch target.Kind() {
		case reflect.Struct:
			out := make(map[string]any, len(typed))
			for key, item := range typed {
				if field, ok := fieldFor(target, key); ok {
					out[key] = coerce(item, field.Type)
				} else {
					out[key] = item
				}
			}
			return out
		case reflect.Map:
			out := make(map[string]any, len(typed))
			for key, item := range typed {
				out[key] = coerce(item, target.Elem())
			}
			return out
		}
	case []any:
		if target.Kind() == reflect.Slice || target.Kind() == reflect.Array {
			out := make([]any, len(typed))
			for index, item := range typed {
				out[index] = coerce(item, target.Elem())
			}
			return out
		}
	case string:
		switch target.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if number, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
				return number
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			if number, err := strconv.ParseUint(strings.TrimSpace(typed), 10, 64); err == nil {
				return number
			}
		case reflect.Float32, reflect.Float64:
			if number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
				return number
			}
		case reflect.Bool:
			if flag, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(typed))); err == nil {
				return flag
			}
		}
	}
	return value
}

// fieldFor 按 JSON 名（json 标签或字段名，不区分大小写）找结构体字段，含嵌入结构体的字段。
func fieldFor(target reflect.Type, key string) (reflect.StructField, bool) {
	for index := range target.NumField() {
		field := target.Field(index)
		if !field.IsExported() {
			continue
		}
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			if nested, ok := fieldFor(field.Type, key); ok {
				return nested, true
			}
			continue
		}
		name := field.Name
		if tag, ok := field.Tag.Lookup("json"); ok {
			tagName, _, _ := strings.Cut(tag, ",")
			if tagName == "-" {
				continue
			}
			if tagName != "" {
				name = tagName
			}
		}
		if strings.EqualFold(name, key) {
			return field, true
		}
	}
	return reflect.StructField{}, false
}
