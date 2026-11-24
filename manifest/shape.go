package manifest

import (
	"strconv"
	"strings"

	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// shapeKind 是 Schema 里一个值的种类
type shapeKind uint8

const (
	shapeString shapeKind = iota
	shapeBoolean
	shapeObject
	shapeArray
)

// valueShape 是一个值的形状：种类、字符串的取值约束、对象的形状、数组项的形状，数组是否至少一项、项是否不得重复
type valueShape struct {
	kind     shapeKind
	accepts  func(string) bool
	object   *objectShape
	item     *valueShape
	nonEmpty bool
	unique   bool
}

// objectShape 是一个对象的形状：允许的成员（区分大小写）与按 Schema 顺序的必填成员
type objectShape struct {
	members  map[string]*valueShape
	required []string
}

// rootShape 是清单根对象的形状，照 manifest.schema.json
var rootShape = buildRootShape()

// findShapeProblem 按 Schema 逐项核对清单 JSON（RFC-0002 解析规则 2，ADR-0236），返回第一处问题的路径；根不是对象时为 $。
// 成员按在文本里出现的顺序核对，每个成员依次查 Schema 之外的成员 → null → 类型 → 取值，对象与数组进到里面再查；
// 一个对象的成员都查完后，按 required 的顺序查缺少的成员，路径是缺少的成员本身
func findShapeProblem(root *jsontext.Value) (string, bool) {
	if root.Kind != jsontext.Object {
		return "$", true
	}
	return checkObject(root, rootShape, "$")
}

// checkObject 核对一个对象
func checkObject(value *jsontext.Value, shape *objectShape, path string) (string, bool) {
	for _, member := range value.Members {
		memberPath := jsontext.MemberPath(path, member.Name)
		memberShape, ok := shape.members[member.Name]
		if !ok {
			return memberPath, true
		}
		if problem, found := checkValue(member.Value, memberShape, memberPath); found {
			return problem, true
		}
	}
	for _, name := range shape.required {
		if _, ok := value.Find(name); !ok {
			return jsontext.MemberPath(path, name), true
		}
	}
	return "", false
}

// checkValue 核对一个值：Schema 里没有哪个成员或数组项能取 null，可选成员缺失表示没有
func checkValue(value *jsontext.Value, shape *valueShape, path string) (string, bool) {
	if value.Kind == jsontext.Null {
		return path, true
	}
	switch shape.kind {
	case shapeString:
		if value.Kind != jsontext.String || (shape.accepts != nil && !shape.accepts(value.Text)) {
			return path, true
		}
	case shapeBoolean:
		if value.Kind != jsontext.Bool {
			return path, true
		}
	case shapeObject:
		if value.Kind != jsontext.Object {
			return path, true
		}
		return checkObject(value, shape.object, path)
	case shapeArray:
		if value.Kind != jsontext.Array || (shape.nonEmpty && len(value.Items) == 0) {
			return path, true
		}
		// 不得重复的字符串数组：先核对这一项本身，再看它是否与前面的项重复，重复时报第二次出现的位置
		var seen map[string]bool
		if shape.unique {
			seen = map[string]bool{}
		}
		for index, item := range value.Items {
			itemPath := jsontext.IndexPath(path, index)
			if problem, found := checkValue(item, shape.item, itemPath); found {
				return problem, true
			}
			if seen != nil {
				if seen[item.Text] {
					return itemPath, true
				}
				seen[item.Text] = true
			}
		}
	}
	return "", false
}

// buildRootShape 按 manifest.schema.json 建出根对象的形状
func buildRootShape() *objectShape {
	anyText := &valueShape{kind: shapeString}
	flag := &valueShape{kind: shapeBoolean}
	text := func(accepts func(string) bool) *valueShape { return &valueShape{kind: shapeString, accepts: accepts} }
	list := func(item *valueShape) *valueShape { return &valueShape{kind: shapeArray, item: item} }
	object := func(shape *objectShape) *valueShape { return &valueShape{kind: shapeObject, object: shape} }
	oneOf := func(names ...string) func(string) bool {
		return func(text string) bool {
			for _, name := range names {
				if text == name {
					return true
				}
			}
			return false
		}
	}

	capability := &objectShape{
		members:  map[string]*valueShape{"id": text(IsCapabilityID), "version": text(isPositiveInt32)},
		required: []string{"id", "version"},
	}
	requirement := &objectShape{
		members: map[string]*valueShape{
			"id":       text(IsCapabilityID),
			"version":  text(func(text string) bool { return isPositiveInt32(strings.TrimPrefix(text, "^")) }),
			"optional": flag,
		},
		required: []string{"id", "version"},
	}
	binding := &objectShape{
		members: map[string]*valueShape{
			"name":     text(bindingNamePattern.MatchString),
			"type":     text(bindingTypePattern.MatchString),
			"required": flag,
		},
		required: []string{"name", "type"},
	}
	pipeline := &objectShape{
		members: map[string]*valueShape{
			"middleware": text(func(text string) bool { return text != "" }),
			"stage":      text(isStageName),
			"before":     list(anyText),
			"after":      list(anyText),
		},
		required: []string{"middleware", "stage"},
	}
	config := &objectShape{
		members:  map[string]*valueShape{"section": text(sectionPattern.MatchString), "schema": anyText},
		required: []string{"section"},
	}
	permissions := list(text(permCodePattern.MatchString))
	permissions.unique = true
	// 操作 ID 的文法 <resource>.<verb>（RFC-0011，连字符只在字母数字之间，整个 ID 不超过 128 个字符）只有 rules.IsOperationID 这一份，
	// operation.IsID 用的也是它
	contributions := &objectShape{
		members: map[string]*valueShape{
			"pipeline":    list(object(pipeline)),
			"operations":  list(text(rules.IsOperationID)),
			"config":      object(config),
			"permissions": permissions,
			"health":      list(text(healthPattern.MatchString)),
		},
	}
	// 激活事件的文法（RFC-0002 解析规则 2 第 3 步，plugin/lifecycle.md §3）也只有 rules.IsActivationEvent 这一份：onOperation: 的模式是
	// 操作 ID 的字符加 *，onEvent: 的模式是主题的字符加 *，所以只有它可以带下划线（ADR-0276）
	activation := list(text(rules.IsActivationEvent))
	activation.nonEmpty = true
	return &objectShape{
		members: map[string]*valueShape{
			"$schema":     anyText,
			"id":          text(IsManifestID),
			"package":     anyText,
			"version":     text(semverPattern.MatchString),
			"primitives":  text(isPositiveInt32),
			"stability":   text(oneOf("stable", "preview", "experimental", "internal", "deprecated")),
			"kind":        text(oneOf(string(KindCapability), string(KindProvider), string(KindPlugin), string(KindHost), string(KindProtocol))),
			"role":        text(oneOf(string(RoleProvider), string(RoleDefault))),
			"isolation":   text(oneOf(string(IsolationInProcess), string(IsolationWasm), string(IsolationOutOfProcess))),
			"provides":    list(object(capability)),
			"requires":    list(object(requirement)),
			"bindings":    list(object(binding)),
			"contributes": object(contributions),
			"activation":  activation,
			"permissions": list(text(permissionPattern.MatchString)),
			"entry":       anyText,
		},
		required: []string{"id", "version", "primitives", "stability", "kind"},
	}
}

// isPositiveInt32 判断是否是不带前导零的正整数文本且不超过 2147483647（ADR-0236）
func isPositiveInt32(text string) bool {
	if !versionPattern.MatchString(text) {
		return false
	}
	_, err := strconv.ParseInt(text, 10, 32)
	return err == nil
}

// isStageName 判断是否是十一个阶段的小写名之一，区分大小写
func isStageName(text string) bool {
	for _, stage := range primitives.PipelineStages() {
		if stage.ManifestName() == text {
			return true
		}
	}
	return false
}
