package manifest

import (
	"strconv"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// write 按规范的排版写出清单（RFC-0002「写出」）
func write(manifest *Manifest) []byte {
	w := &indentedWriter{}
	w.startObject()
	w.member("$schema").text(SchemaURI)
	w.member("id").text(manifest.ID)
	if manifest.Package != "" {
		w.member("package").text(manifest.Package)
	}
	w.member("version").text(manifest.Version)
	w.member("primitives").text(strconv.Itoa(int(manifest.Primitives)))
	w.member("stability").text(manifest.Stability.String())
	w.member("kind").text(string(manifest.Kind))
	w.member("role").text(string(manifest.EffectiveRole()))
	if manifest.Isolation != "" {
		w.member("isolation").text(string(manifest.Isolation))
	}
	w.member("provides").startArray()
	for _, declaration := range manifest.Provides {
		w.item().startObject()
		w.member("id").text(declaration.ID)
		w.member("version").text(strconv.Itoa(int(declaration.Version)))
		w.end('}')
	}
	w.end(']')
	w.member("requires").startArray()
	for _, requirement := range manifest.Requires {
		w.item().startObject()
		w.member("id").text(requirement.ID)
		w.member("version").text(strconv.Itoa(int(requirement.Version)))
		w.member("optional").boolean(requirement.Optional)
		w.end('}')
	}
	w.end(']')
	w.member("bindings").startArray()
	for _, binding := range manifest.Bindings {
		w.item().startObject()
		w.member("name").text(binding.Name)
		w.member("type").text(binding.Type)
		w.member("required").boolean(binding.IsRequired())
		w.end('}')
	}
	w.end(']')
	contributes := manifest.Contributes
	if contributes == nil {
		contributes = &Contributions{}
	}
	w.member("contributes").startObject()
	w.member("pipeline").startArray()
	for _, contribution := range contributes.Pipeline {
		w.item().startObject()
		w.member("middleware").text(contribution.Middleware)
		w.member("stage").text(contribution.Stage.ManifestName())
		w.member("before").texts(contribution.Before)
		w.member("after").texts(contribution.After)
		w.end('}')
	}
	w.end(']')
	w.member("operations").texts(contributes.Operations)
	if config := contributes.Config; config != nil {
		w.member("config").startObject()
		w.member("section").text(config.Section)
		if config.Schema != "" {
			w.member("schema").text(config.Schema)
		}
		w.end('}')
	}
	w.member("permissions").texts(contributes.Permissions)
	w.member("health").texts(contributes.Health)
	w.end('}')
	w.member("activation").texts(manifest.EffectiveActivation())
	w.member("permissions").texts(manifest.Permissions)
	if manifest.Entry != "" {
		w.member("entry").text(manifest.Entry)
	}
	w.end('}')
	return w.buffer
}

// indentedWriter 按规范排版写 JSON：每一层记是否已写过成员或项；只覆盖清单用到的写法
type indentedWriter struct {
	buffer  []byte
	written []bool
}

// startObject 开始一个对象
func (w *indentedWriter) startObject() *indentedWriter {
	w.buffer = append(w.buffer, '{')
	w.written = append(w.written, false)
	return w
}

// startArray 开始一个数组
func (w *indentedWriter) startArray() *indentedWriter {
	w.buffer = append(w.buffer, '[')
	w.written = append(w.written, false)
	return w
}

// member 写成员名
func (w *indentedWriter) member(name string) *indentedWriter {
	w.item()
	w.buffer = jsontext.AppendString(w.buffer, name)
	w.buffer = append(w.buffer, ':', ' ')
	return w
}

// item 在成员或数组项之前写逗号、换行与缩进
func (w *indentedWriter) item() *indentedWriter {
	last := len(w.written) - 1
	if w.written[last] {
		w.buffer = append(w.buffer, ',')
	}
	w.written[last] = true
	w.newline(len(w.written))
	return w
}

// end 结束一层：有内容时换行并按外层缩进；空数组写成 []
func (w *indentedWriter) end(bracket byte) {
	last := len(w.written) - 1
	wrote := w.written[last]
	w.written = w.written[:last]
	if wrote {
		w.newline(len(w.written))
	}
	w.buffer = append(w.buffer, bracket)
}

// newline 换行并缩进 depth 层（每层两个空格）
func (w *indentedWriter) newline(depth int) {
	w.buffer = append(w.buffer, '\n')
	for index := 0; index < depth; index++ {
		w.buffer = append(w.buffer, ' ', ' ')
	}
}

// text 写字符串值
func (w *indentedWriter) text(value string) { w.buffer = jsontext.AppendString(w.buffer, value) }

// boolean 写布尔值
func (w *indentedWriter) boolean(value bool) { w.buffer = strconv.AppendBool(w.buffer, value) }

// texts 写字符串数组
func (w *indentedWriter) texts(values []string) {
	w.startArray()
	for _, value := range values {
		w.item().text(value)
	}
	w.end(']')
}
