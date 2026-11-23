package plugin

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// anyType 是 any 的类型：目标没有更具体的类型时按它改写，不做转换。
var anyType = reflect.TypeFor[any]()

// Decode 把配置节解码到 target（指向结构体的指针），按 lifecycle.md §7.2 的类型化读取：宿主的配置源是键值字符串（.NET 的 IConfiguration），
// 数字与布尔都以 JSON 字符串下发，所以先按 target 的字段类型改写，再交给 encoding/json：
//   - 成员名按 ASCII 不区分大小写匹配字段的 JSON 名（与 IConfiguration 一致），名字完全相同的优先；没有对应字段的成员丢掉；
//   - 落在数字字段上的字符串合 JSON 数字的文法（不带空白、正号与前导零）时当作那个数字，再照目标类型的常规读法（整数字段读 1.5 照样报错）；
//   - 落在布尔字段上的字符串只认 true、false（ASCII 不区分大小写）；
//   - 其余原样，本来就是数字或布尔的 JSON 也原样接受；实现了 json.Unmarshaler 或 encoding.TextUnmarshaler 的类型整段原样交给它。
//
// 空配置节解码为零值。
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
	root, problem := jsontext.Parse(c.Section)
	if problem != nil {
		return fmt.Errorf("plugin: the configuration section is not valid JSON: %w", problem)
	}
	normalized, err := json.Marshal(normalize(c.Section, root, pointer.Type().Elem()))
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}

// unmarshalerTypes 是自己解码 JSON 的接口：目标实现了它们时不按字段改写。
var (
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// normalize 按目标类型把配置节改写成 encoding/json 能照字面读的值：数字保留原文（json.Number），不经过 float64。
func normalize(data []byte, value *jsontext.Value, target reflect.Type) any {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() != reflect.Interface && (reflect.PointerTo(target).Implements(jsonUnmarshalerType) || reflect.PointerTo(target).Implements(textUnmarshalerType)) {
		return json.RawMessage(data[value.Start:value.End])
	}
	switch value.Kind {
	case jsontext.Bool:
		return value.Bool
	case jsontext.Number:
		return json.Number(value.Text)
	case jsontext.String:
		return coerceString(value.Text, target)
	case jsontext.Array:
		element := anyType
		if target.Kind() == reflect.Slice || target.Kind() == reflect.Array {
			element = target.Elem()
		}
		items := make([]any, len(value.Items))
		for index, item := range value.Items {
			items[index] = normalize(data, item, element)
		}
		return items
	case jsontext.Object:
		members := make(map[string]any, len(value.Members))
		switch target.Kind() {
		case reflect.Struct:
			// 按文本顺序处理，大小写不同的同名成员后出现的为准；没有对应字段的成员丢掉，留着 encoding/json 会按 Unicode 的大小写折叠
			// （如 K 与开尔文符号 U+212A）把它匹配到字段上，不再是 ASCII 不区分大小写
			for _, member := range value.Members {
				if name, field, ok := fieldFor(target, member.Name); ok {
					members[name] = normalize(data, member.Value, field.Type)
				}
			}
		case reflect.Map:
			for _, member := range value.Members {
				members[member.Name] = normalize(data, member.Value, target.Elem())
			}
		default:
			for _, member := range value.Members {
				members[member.Name] = normalize(data, member.Value, anyType)
			}
		}
		return members
	default:
		return nil
	}
}

// coerceString 按目标类型转换字符串叶子：数字按 JSON 数字的文法，布尔只认 true、false（ASCII 不区分大小写）；转不了的原样保留，
// 让 encoding/json 报它自己的错误。
func coerceString(text string, target reflect.Type) any {
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		// 整个文本是一个 JSON 数字（RFC 8259）才转：不带首尾空白、正号与前导零，NaN 与 Infinity 不算
		if jsontext.IsNumber(text) {
			return json.Number(text)
		}
	case reflect.Bool:
		switch {
		case asciiEqualFold(text, "true"):
			return true
		case asciiEqualFold(text, "false"):
			return false
		}
	}
	return text
}

// fieldFor 按成员名找结构体字段（含嵌入结构体提升的字段），返回字段的 JSON 名：名字完全相同的优先，
// 其次按 ASCII 不区分大小写匹配（lifecycle.md §7.2）。
func fieldFor(target reflect.Type, name string) (string, reflect.StructField, bool) {
	if jsonName, field, ok := findField(target, name, func(a, b string) bool { return a == b }); ok {
		return jsonName, field, true
	}
	return findField(target, name, asciiEqualFold)
}

// findField 按 equal 找结构体字段：json 标签为 - 的与未导出的字段跳过，没有标签名的匿名结构体字段把它的字段提升上来。
func findField(target reflect.Type, name string, equal func(a, b string) bool) (string, reflect.StructField, bool) {
	for index := range target.NumField() {
		field := target.Field(index)
		tagName, hasTagName := "", false
		if tag, ok := field.Tag.Lookup("json"); ok {
			tagName, _, _ = strings.Cut(tag, ",")
			if tagName == "-" {
				continue
			}
			hasTagName = tagName != ""
		}
		embedded := field.Type
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		if field.Anonymous && !hasTagName && embedded.Kind() == reflect.Struct {
			if jsonName, nested, ok := findField(embedded, name, equal); ok {
				return jsonName, nested, true
			}
			continue
		}
		if !field.IsExported() {
			continue
		}
		jsonName := field.Name
		if hasTagName {
			jsonName = tagName
		}
		if equal(jsonName, name) {
			return jsonName, field, true
		}
	}
	return "", reflect.StructField{}, false
}

// asciiEqualFold 按 ASCII 不区分大小写比较：只有 A–Z 与 a–z 互相折叠，其余字节必须相同。
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := 0; index < len(a); index++ {
		x, y := a[index], b[index]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
