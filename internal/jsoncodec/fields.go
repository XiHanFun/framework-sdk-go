// Package jsoncodec 是 SDK 读写 JSON 值的共用实现，建在 internal/jsontext 读出的、按文档顺序的值上：
//   - 值模型（RFC-0010「模板」，ADR-0236）：JSON 的数字在 64 位有符号范围内的整数写法读成 int64，其余读成 float64，
//     写出时整数值的浮点带小数点（2.0、-0.0），非有限值写成字符串 NaN、Infinity、-Infinity，往返后仍是同一个值
//   - 按 Go 类型解码（RFC-0011 规则 2）：成员名区分大小写、未知成员忽略、数字不从字符串读、整数不接受小数点与指数、
//     超出范围即不符、浮点上溢为无穷；整数、浮点与布尔不接受 null；带 xihan:"required" 的成员缺少时在所在对象结束时报；
//     按文本顺序报第一处问题的路径；any 是任意 JSON，数字保留原文（json.Number）
//
// 结构体字段的名字、嵌入与 omitempty / omitzero / string 选项照 encoding/json 的规则
package jsoncodec

import (
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// field 是结构体的一个 JSON 成员
type field struct {
	name      string
	index     []int
	typ       reflect.Type
	tagged    bool
	omitEmpty bool
	omitZero  bool
	quoted    bool
	required  bool
}

// fieldCache 按类型缓存成员表
var fieldCache sync.Map

// fieldsOf 返回结构体的 JSON 成员，按声明顺序；嵌入结构体的成员照 encoding/json 的规则提升：同名时层次浅的胜出，
// 同一层有多个时只有一个带标签的胜出，否则都不要
func fieldsOf(t reflect.Type) []field {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.([]field)
	}
	fields := typeFields(t)
	actual, _ := fieldCache.LoadOrStore(t, fields)
	return actual.([]field)
}

// typeFields 按层次广度优先收集成员（encoding/json 的 typeFields）
func typeFields(t reflect.Type) []field {
	current := []field{}
	next := []field{{typ: t}}
	var count, nextCount map[reflect.Type]int
	visited := map[reflect.Type]bool{}
	var fields []field
	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}
		for _, outer := range current {
			if visited[outer.typ] {
				continue
			}
			visited[outer.typ] = true
			for index := 0; index < outer.typ.NumField(); index++ {
				structField := outer.typ.Field(index)
				if structField.Anonymous {
					embedded := structField.Type
					if embedded.Kind() == reflect.Pointer {
						embedded = embedded.Elem()
					}
					if !structField.IsExported() && embedded.Kind() != reflect.Struct {
						continue
					}
				} else if !structField.IsExported() {
					continue
				}
				tag := structField.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, options, _ := strings.Cut(tag, ",")
				if !validName(name) {
					name = ""
				}
				path := make([]int, len(outer.index)+1)
				copy(path, outer.index)
				path[len(outer.index)] = index
				typ := structField.Type
				if typ.Name() == "" && typ.Kind() == reflect.Pointer {
					typ = typ.Elem()
				}
				if name != "" || !structField.Anonymous || typ.Kind() != reflect.Struct {
					member := field{
						name:      name,
						index:     path,
						typ:       structField.Type,
						tagged:    name != "",
						omitEmpty: hasOption(options, "omitempty"),
						omitZero:  hasOption(options, "omitzero"),
						required:  hasOption(structField.Tag.Get("xihan"), "required"),
					}
					if member.name == "" {
						member.name = structField.Name
					}
					if hasOption(options, "string") {
						switch typ.Kind() {
						case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
							reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
							reflect.Float32, reflect.Float64:
							member.quoted = true
						}
					}
					fields = append(fields, member)
					if count[outer.typ] > 1 {
						// 同一层嵌入了两次同一类型：两份互相抵消
						fields = append(fields, fields[len(fields)-1])
					}
					continue
				}
				nextCount[typ]++
				if nextCount[typ] == 1 {
					next = append(next, field{name: typ.Name(), index: path, typ: typ})
				}
			}
		}
	}
	sort.SliceStable(fields, func(i, j int) bool {
		if fields[i].name != fields[j].name {
			return fields[i].name < fields[j].name
		}
		if len(fields[i].index) != len(fields[j].index) {
			return len(fields[i].index) < len(fields[j].index)
		}
		if fields[i].tagged != fields[j].tagged {
			return fields[i].tagged
		}
		return lessIndex(fields[i].index, fields[j].index)
	})
	var dominant []field
	for start := 0; start < len(fields); {
		end := start + 1
		for end < len(fields) && fields[end].name == fields[start].name {
			end++
		}
		if winner, ok := dominantField(fields[start:end]); ok {
			dominant = append(dominant, winner)
		}
		start = end
	}
	sort.Slice(dominant, func(i, j int) bool { return lessIndex(dominant[i].index, dominant[j].index) })
	return dominant
}

// dominantField 在同名的成员里选出胜出的那个：层次最浅且只有一个，或层次最浅的里只有一个带标签
func dominantField(fields []field) (field, bool) {
	if len(fields) > 1 && len(fields[0].index) == len(fields[1].index) && fields[0].tagged == fields[1].tagged {
		return field{}, false
	}
	return fields[0], true
}

// lessIndex 按字段下标的字典序比较
func lessIndex(left, right []int) bool {
	for index := range left {
		if index >= len(right) {
			return false
		}
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return len(left) < len(right)
}

// hasOption 判断逗号分隔的选项里有没有 option
func hasOption(options, option string) bool {
	for options != "" {
		var current string
		current, options, _ = strings.Cut(options, ",")
		if current == option {
			return true
		}
	}
	return false
}

// validName 判断标签里的名字是否可用（encoding/json 的 isValidTag）
func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}
