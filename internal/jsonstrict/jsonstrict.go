// Package jsonstrict 是读取线上 JSON 时各包共用的严格核对（RFC-0001「Result」、RFC-0002 解析规则 2，ADR-0235）：
// encoding/json 解码时后出现的同名成员会悄悄覆盖前一个，同一层的重复成员名要在词法上单独查
package jsonstrict

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// scope 是读到的一层容器：对象记成员名，以及下一个记号是不是成员名
type scope struct {
	names     map[string]bool
	expectKey bool
}

// DuplicateMember 返回 JSON 文本里第一个在同一个对象里重复出现的成员名（按解码后的名字比较，"a" 与 "a" 相同）；
// 没有重复时第二个返回值为假，文本不是合法 JSON 时返回解析错误
func DuplicateMember(data []byte) (string, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var stack []*scope
	valueDone := func() {
		if len(stack) > 0 && stack[len(stack)-1].names != nil {
			stack[len(stack)-1].expectKey = true
		}
	}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			// Token 在对象或数组读到一半时也只报 EOF：还有没闭合的容器就是截断的文本
			if len(stack) > 0 {
				return "", false, io.ErrUnexpectedEOF
			}
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if len(stack) > 0 && stack[len(stack)-1].expectKey {
			top := stack[len(stack)-1]
			if delim, ok := token.(json.Delim); ok && delim == '}' {
				stack = stack[:len(stack)-1]
				valueDone()
				continue
			}
			name, _ := token.(string)
			if top.names[name] {
				return name, true, nil
			}
			top.names[name] = true
			top.expectKey = false
			continue
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				stack = append(stack, &scope{names: map[string]bool{}, expectKey: true})
			case '[':
				stack = append(stack, &scope{})
			case '}', ']':
				stack = stack[:len(stack)-1]
				valueDone()
			}
			continue
		}
		valueDone()
	}
}
