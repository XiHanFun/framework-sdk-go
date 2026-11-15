package manifest

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// requiredMembers 是 Schema 里各对象的必填成员（manifest.schema.json 的 required）
var requiredMembers = map[reflect.Type][]string{
	reflect.TypeFor[Manifest]():              {"id", "version", "primitives", "stability", "kind"},
	reflect.TypeFor[CapabilityDeclaration](): {"id", "version"},
	reflect.TypeFor[CapabilityRequirement](): {"id", "version"},
	reflect.TypeFor[Binding]():               {"name", "type"},
	reflect.TypeFor[PipelineContribution]():  {"middleware", "stage"},
	reflect.TypeFor[ConfigContribution]():    {"section"},
}

// checkShape 按模型的类型核对清单 JSON 的形状（Schema 的 additionalProperties: false 与 required）：成员名区分大小写、
// 只能是 Schema 里有的（encoding/json 按名字匹配时不区分大小写，这里先核对），必填成员不能缺，任何位置都不能是 null；
// 标量的类型与文法由解码与 Validate 核对
func checkShape(raw json.RawMessage, t reflect.Type, path string) error {
	if string(raw) == "null" {
		return invalid("'%s' must not be null", path)
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if len(raw) == 0 || raw[0] != '{' {
			return invalid("'%s' must be a JSON object", path)
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			return invalid("'%s' is not a valid JSON object", path)
		}
		fields := jsonFields(t)
		for name, value := range members {
			field, ok := fields[name]
			if !ok {
				return invalid("'%s' has the member '%s', which the schema does not allow (member names are case-sensitive)", path, name)
			}
			if err := checkShape(value, field, path+"."+name); err != nil {
				return err
			}
		}
		for _, name := range requiredMembers[t] {
			if _, ok := members[name]; !ok {
				return invalid("'%s' needs the member '%s'", path, name)
			}
		}
	case reflect.Slice:
		if len(raw) == 0 || raw[0] != '[' {
			return invalid("'%s' must be a JSON array", path)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return invalid("'%s' is not a valid JSON array", path)
		}
		for index, item := range items {
			if err := checkShape(item, t.Elem(), path+"["+strconv.Itoa(index)+"]"); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonFields 返回结构体的 JSON 成员名到字段类型的映射（按 json 标签，区分大小写）
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for index := 0; index < t.NumField(); index++ {
		field := t.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}
