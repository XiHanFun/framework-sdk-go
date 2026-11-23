// Package jsontext 是 XiHan 读取 JSON 文本的共同检查（RFC-0001「JSON 文本」，ADR-0236）：Result 与 Error 的线上编码、操作载荷与清单
// 都先过这一遍，再按文档顺序交给各自的读取。语法按 RFC 8259，嵌套不超过 64 层，字符串必须是合法的 Unicode，任何一层都不能有重复成员；
// 语法错误不论出现在哪里都优先，位置只写 $，重复成员写第一处重复的路径。encoding/json 不查重复成员、把孤立的代理项换成 U+FFFD、
// 嵌套上限是 10000，所以不用它做这一步
package jsontext

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxDepth 是允许的最大嵌套层数，最外层的对象或数组是第 1 层
const MaxDepth = 64

// Kind 是 JSON 值的种类
type Kind uint8

// JSON 值的六种种类
const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value 是按文档顺序读出的一个 JSON 值
type Value struct {
	// Kind 是种类
	Kind Kind
	// Bool 是布尔值
	Bool bool
	// Text 是数字的原文，或字符串还原转义后的文本
	Text string
	// Items 是数组的项
	Items []*Value
	// Members 是对象的成员，按文本里出现的顺序；通过检查的文本没有重复的成员名
	Members []Member
	// Start 与 End 是这个值在原文里的字节范围
	Start, End int
}

// Member 是对象的一个成员
type Member struct {
	Name  string
	Value *Value
}

// Find 按名字找对象的成员，区分大小写
func (v *Value) Find(name string) (*Value, bool) {
	for _, member := range v.Members {
		if member.Name == name {
			return member.Value, true
		}
	}
	return nil, false
}

// Problem 是文本没有通过共同检查的原因
type Problem struct {
	// Syntax 为真表示不是 JSON（RFC 8259 之外的写法、BOM、嵌套过深、非法 UTF-8、孤立的代理项）；否则是重复成员
	Syntax bool
	// Path 在语法错误时为 $，重复成员时为第一处重复（第二次出现的成员）的路径
	Path string
}

// Error 便于日志
func (p *Problem) Error() string {
	if p.Syntax {
		return "the text is not JSON"
	}
	return "the member '" + p.Path + "' appears more than once"
}

// Check 按共同检查核对 UTF-8 文本，通过时返回 nil
func Check(data []byte) *Problem {
	_, problem := Parse(data)
	return problem
}

// Parse 核对文本并按文档顺序读出根值；没有通过检查时返回原因。语法错误优先于重复成员，所以遇到重复成员也读到末尾
func Parse(data []byte) (*Value, *Problem) {
	reader := parser{data: data}
	reader.whitespace()
	root, ok := reader.value(0, nil)
	if ok {
		reader.whitespace()
		ok = reader.at == len(data)
	}
	if !ok {
		return nil, &Problem{Syntax: true, Path: "$"}
	}
	if reader.duplicate != "" {
		return nil, &Problem{Path: reader.duplicate}
	}
	return root, nil
}

// IsNumber 判断文本整个是一个 JSON 数字（RFC 8259）：不带首尾空白、正号与前导零，NaN 与 Infinity 不算。
// Parse 允许值的前后有空白，" 1" 也读得出数字，所以判断「是不是数字」用它，不看 Parse 读出的种类
func IsNumber(text string) bool {
	if text == "" {
		return false
	}
	reader := parser{data: []byte(text)}
	return reader.number() && reader.at == len(reader.data)
}

// frame 是读到的一层位置：父层与本层的成员名或下标，只在需要写路径时展开
type frame struct {
	parent *frame
	name   string
	index  int
	member bool
}

// path 写出位置的路径
func (f *frame) path() string {
	if f == nil {
		return "$"
	}
	if f.member {
		return MemberPath(f.parent.path(), f.name)
	}
	return IndexPath(f.parent.path(), f.index)
}

// parser 是按 RFC 8259 逐字节读取的检查器
type parser struct {
	data      []byte
	at        int
	duplicate string
}

// whitespace 跳过 JSON 的四种空白
func (p *parser) whitespace() {
	for p.at < len(p.data) {
		switch p.data[p.at] {
		case ' ', '\t', '\n', '\r':
			p.at++
		default:
			return
		}
	}
}

// value 读一个值；depth 是外层容器的层数
func (p *parser) value(depth int, at *frame) (*Value, bool) {
	if p.at >= len(p.data) {
		return nil, false
	}
	start := p.at
	switch c := p.data[p.at]; {
	case c == '{':
		return p.object(depth+1, at)
	case c == '[':
		return p.array(depth+1, at)
	case c == '"':
		text, ok := p.text()
		return &Value{Kind: String, Text: text, Start: start, End: p.at}, ok
	case c == '-' || (c >= '0' && c <= '9'):
		if !p.number() {
			return nil, false
		}
		return &Value{Kind: Number, Text: string(p.data[start:p.at]), Start: start, End: p.at}, true
	case p.literal("true"):
		return &Value{Kind: Bool, Bool: true, Start: start, End: p.at}, true
	case p.literal("false"):
		return &Value{Kind: Bool, Start: start, End: p.at}, true
	case p.literal("null"):
		return &Value{Kind: Null, Start: start, End: p.at}, true
	}
	return nil, false
}

// object 读一个对象；重复的成员名只记第一处
func (p *parser) object(depth int, at *frame) (*Value, bool) {
	if depth > MaxDepth {
		return nil, false
	}
	result := &Value{Kind: Object, Start: p.at}
	p.at++
	p.whitespace()
	if p.at < len(p.data) && p.data[p.at] == '}' {
		p.at++
		result.End = p.at
		return result, true
	}
	var names map[string]struct{}
	for {
		if p.at >= len(p.data) || p.data[p.at] != '"' {
			return nil, false
		}
		name, ok := p.text()
		if !ok {
			return nil, false
		}
		member := &frame{parent: at, name: name, member: true}
		if p.duplicate == "" {
			if names == nil {
				names = map[string]struct{}{}
			}
			if _, seen := names[name]; seen {
				p.duplicate = member.path()
			}
			names[name] = struct{}{}
		}
		p.whitespace()
		if p.at >= len(p.data) || p.data[p.at] != ':' {
			return nil, false
		}
		p.at++
		p.whitespace()
		item, ok := p.value(depth, member)
		if !ok {
			return nil, false
		}
		result.Members = append(result.Members, Member{Name: name, Value: item})
		p.whitespace()
		if p.at >= len(p.data) {
			return nil, false
		}
		switch p.data[p.at] {
		case ',':
			p.at++
			p.whitespace()
		case '}':
			p.at++
			result.End = p.at
			return result, true
		default:
			return nil, false
		}
	}
}

// array 读一个数组
func (p *parser) array(depth int, at *frame) (*Value, bool) {
	if depth > MaxDepth {
		return nil, false
	}
	result := &Value{Kind: Array, Start: p.at}
	p.at++
	p.whitespace()
	if p.at < len(p.data) && p.data[p.at] == ']' {
		p.at++
		result.End = p.at
		return result, true
	}
	for index := 0; ; index++ {
		item, ok := p.value(depth, &frame{parent: at, index: index})
		if !ok {
			return nil, false
		}
		result.Items = append(result.Items, item)
		p.whitespace()
		if p.at >= len(p.data) {
			return nil, false
		}
		switch p.data[p.at] {
		case ',':
			p.at++
			p.whitespace()
		case ']':
			p.at++
			result.End = p.at
			return result, true
		default:
			return nil, false
		}
	}
}

// text 读一个字符串并还原转义：非法 UTF-8（含过长编码与编码成 UTF-8 的代理项）、原始控制字符、非法转义与孤立的代理项都不合法
func (p *parser) text() (string, bool) {
	p.at++
	start := p.at
	// 没有转义时直接取原文
	for p.at < len(p.data) {
		c := p.data[p.at]
		switch {
		case c == '"':
			p.at++
			return string(p.data[start : p.at-1]), true
		case c == '\\':
			return p.escaped(start)
		case c < 0x20:
			return "", false
		case c < utf8.RuneSelf:
			p.at++
		default:
			if !p.rune() {
				return "", false
			}
		}
	}
	return "", false
}

// escaped 从第一个转义起读完字符串，前面没有转义的部分是 data[start:at]
func (p *parser) escaped(start int) (string, bool) {
	var builder strings.Builder
	builder.Write(p.data[start:p.at])
	for p.at < len(p.data) {
		c := p.data[p.at]
		switch {
		case c == '"':
			p.at++
			return builder.String(), true
		case c == '\\':
			if !p.escape(&builder) {
				return "", false
			}
		case c < 0x20:
			return "", false
		case c < utf8.RuneSelf:
			builder.WriteByte(c)
			p.at++
		default:
			from := p.at
			if !p.rune() {
				return "", false
			}
			builder.Write(p.data[from:p.at])
		}
	}
	return "", false
}

// rune 读一个多字节的 UTF-8 字符；utf8.DecodeRune 对非法序列、过长编码与编码成 UTF-8 的代理项都返回长度 1 的 RuneError
func (p *parser) rune() bool {
	decoded, size := utf8.DecodeRune(p.data[p.at:])
	if decoded == utf8.RuneError && size <= 1 {
		return false
	}
	p.at += size
	return true
}

// escape 读一个转义序列；高代理项后必须紧跟低代理项的转义，单独的低代理项不合法
func (p *parser) escape(builder *strings.Builder) bool {
	if p.at+1 >= len(p.data) {
		return false
	}
	switch p.data[p.at+1] {
	case '"', '\\', '/':
		builder.WriteByte(p.data[p.at+1])
	case 'b':
		builder.WriteByte('\b')
	case 'f':
		builder.WriteByte('\f')
	case 'n':
		builder.WriteByte('\n')
	case 'r':
		builder.WriteByte('\r')
	case 't':
		builder.WriteByte('\t')
	case 'u':
		unit, ok := p.hex4(p.at + 2)
		if !ok {
			return false
		}
		switch {
		case unit >= 0xD800 && unit < 0xDC00:
			if p.at+7 >= len(p.data) || p.data[p.at+6] != '\\' || p.data[p.at+7] != 'u' {
				return false
			}
			low, ok := p.hex4(p.at + 8)
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			builder.WriteRune(utf16.DecodeRune(rune(unit), rune(low)))
			p.at += 12
			return true
		case unit >= 0xDC00 && unit <= 0xDFFF:
			return false
		}
		builder.WriteRune(rune(unit))
		p.at += 6
		return true
	default:
		return false
	}
	p.at += 2
	return true
}

// hex4 读 from 起的四位十六进制数
func (p *parser) hex4(from int) (uint16, bool) {
	if from+4 > len(p.data) {
		return 0, false
	}
	var value uint16
	for _, c := range p.data[from : from+4] {
		switch {
		case c >= '0' && c <= '9':
			value = value<<4 | uint16(c-'0')
		case c >= 'a' && c <= 'f':
			value = value<<4 | uint16(c-'a'+10)
		case c >= 'A' && c <= 'F':
			value = value<<4 | uint16(c-'A'+10)
		default:
			return 0, false
		}
	}
	return value, true
}

// number 读一个数：-? (0 | [1-9][0-9]*) (.[0-9]+)? ([eE][+-]?[0-9]+)?
func (p *parser) number() bool {
	if p.data[p.at] == '-' {
		p.at++
	}
	if p.at >= len(p.data) {
		return false
	}
	if p.data[p.at] == '0' {
		p.at++
	} else if !p.digits() {
		return false
	}
	if p.at < len(p.data) && p.data[p.at] == '.' {
		p.at++
		if !p.digits() {
			return false
		}
	}
	if p.at < len(p.data) && (p.data[p.at] == 'e' || p.data[p.at] == 'E') {
		p.at++
		if p.at < len(p.data) && (p.data[p.at] == '+' || p.data[p.at] == '-') {
			p.at++
		}
		if !p.digits() {
			return false
		}
	}
	return true
}

// digits 读至少一位十进制数字
func (p *parser) digits() bool {
	start := p.at
	for p.at < len(p.data) && p.data[p.at] >= '0' && p.data[p.at] <= '9' {
		p.at++
	}
	return p.at > start
}

// literal 读一个字面量
func (p *parser) literal(word string) bool {
	if len(p.data)-p.at >= len(word) && string(p.data[p.at:p.at+len(word)]) == word {
		p.at += len(word)
		return true
	}
	return false
}

// bracketedNameCharacters 是成员名里要写成 ['名字'] 的字符（RFC-0011 规则 2，照 System.Text.Json 的实测）：
// . 空格 ' / " [ ] ( ) \ 制表 换行 回车 换页 退格 U+0085 U+2028 U+2029；其余字符照写，名字原样、不转义
const bracketedNameCharacters = ". '/\"[]()\\\t\n\r\f\b\u0085\u2028\u2029"

// MemberPath 在路径后接一个成员：名字含 bracketedNameCharacters 里的字符时写 ['名字']，否则写 .名字（空名字写成 .）
func MemberPath(parent, name string) string {
	if strings.ContainsAny(name, bracketedNameCharacters) {
		return parent + "['" + name + "']"
	}
	return parent + "." + name
}

// IndexPath 在路径后接一个数组下标
func IndexPath(parent string, index int) string { return parent + "[" + strconv.Itoa(index) + "]" }
