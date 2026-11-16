package jsontext

import "unicode/utf8"

// hexDigits 是转义用的小写十六进制数字
const hexDigits = "0123456789abcdef"

// AppendString 按 RFC-0010「模板」的 JSON 字符串写法写一个字符串（清单写出用同一种写法，ADR-0236）：" 与 \ 加反斜杠，
// 退格、换页、换行、回车、制表符用短写，其余 U+0000–U+001F 写成小写十六进制的 \u001f；别的字符（含 /、U+007F–U+009F、
// U+2028 与非 ASCII）原样。Go 的字符串里，按 WTF-8 编码的孤立代理项（ED A0–BF 80–BF）写成 \ud800 这样的转义，
// 其余非法的 UTF-8 字节写成 U+FFFD
func AppendString(dst []byte, text string) []byte {
	dst = append(dst, '"')
	for index := 0; index < len(text); {
		c := text[index]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				dst = append(dst, '\\', '"')
			case '\\':
				dst = append(dst, '\\', '\\')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				if c < 0x20 {
					dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
				} else {
					dst = append(dst, c)
				}
			}
			index++
			continue
		}
		decoded, size := utf8.DecodeRuneInString(text[index:])
		if decoded == utf8.RuneError && size == 1 {
			if unit, ok := surrogate(text[index:]); ok {
				dst = append(dst, '\\', 'u', hexDigits[unit>>12], hexDigits[unit>>8&0xF], hexDigits[unit>>4&0xF], hexDigits[unit&0xF])
				index += 3
				continue
			}
			dst = utf8.AppendRune(dst, utf8.RuneError)
			index++
			continue
		}
		dst = append(dst, text[index:index+size]...)
		index += size
	}
	return append(dst, '"')
}

// surrogate 读按 WTF-8 编码的一个代理项码元：ED A0–BF 80–BF
func surrogate(text string) (uint16, bool) {
	if len(text) < 3 || text[0] != 0xED || text[1] < 0xA0 || text[1] > 0xBF || text[2] < 0x80 || text[2] > 0xBF {
		return 0, false
	}
	return 0xD000 | uint16(text[1]&0x3F)<<6 | uint16(text[2]&0x3F), true
}
