package plugin

import (
	"io"
	"log/slog"
	"strings"
)

// NewLogHandler 创建把日志写成宿主可解析的 JSON 行的 slog 处理器（invocation.md §6）：
// 每行一个对象，level 为 trace / debug / information / warning / error / critical，message 是文案，其余字段为作用域属性。
func NewLogHandler(target io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(target, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			switch attr.Key {
			case slog.TimeKey:
				return slog.Attr{}
			case slog.MessageKey:
				return slog.String("message", attr.Value.String())
			case slog.LevelKey:
				if level, ok := attr.Value.Any().(slog.Level); ok {
					return slog.String("level", levelName(level))
				}
				return slog.String("level", strings.ToLower(attr.Value.String()))
			default:
				return attr
			}
		},
	})
}

// levelName 把 slog 级别映射为宿主的级别名。
func levelName(level slog.Level) string {
	switch {
	case level < slog.LevelDebug:
		return "trace"
	case level < slog.LevelInfo:
		return "debug"
	case level < slog.LevelWarn:
		return "information"
	case level < slog.LevelError:
		return "warning"
	case level < slog.LevelError+4:
		return "error"
	default:
		return "critical"
	}
}
