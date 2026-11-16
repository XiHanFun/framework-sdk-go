package jsoncodec

import (
	"errors"
	"strconv"
	"strings"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// FromJSON 把 JSON 值读成值模型（RFC-0010「模板」，ADR-0236）：null 为 nil，布尔为 bool，字符串为 string，
// 数字按 Number，数组为 []any，对象为 map[string]any
func FromJSON(value *jsontext.Value) any {
	switch value.Kind {
	case jsontext.Bool:
		return value.Bool
	case jsontext.Number:
		return Number(value.Text)
	case jsontext.String:
		return value.Text
	case jsontext.Array:
		items := make([]any, len(value.Items))
		for index, item := range value.Items {
			items[index] = FromJSON(item)
		}
		return items
	case jsontext.Object:
		members := make(map[string]any, len(value.Members))
		for _, member := range value.Members {
			members[member.Name] = FromJSON(member.Value)
		}
		return members
	}
	return nil
}

// Number 把合法的 JSON 数字文本读成值模型：整数写法（没有小数点与指数）且在 64 位有符号范围内是 int64（-0 是整数 0），
// 其余是 float64，上溢为无穷、下溢为 0
func Number(text string) any {
	if isIntegerText(text) {
		if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
			return integer
		}
	}
	return parseFloat(text, 64)
}

// isIntegerText 判断数字文本是不是整数写法
func isIntegerText(text string) bool { return !strings.ContainsAny(text, ".eE") }

// parseFloat 按 bitSize 读浮点：超出范围时 strconv 返回无穷或 0 与 ErrRange，照 System.Text.Json 接受这个值
func parseFloat(text string, bitSize int) float64 {
	value, err := strconv.ParseFloat(text, bitSize)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0
	}
	return value
}
