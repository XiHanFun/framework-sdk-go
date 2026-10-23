package plugin

import (
	"io"
	"log/slog"
)

// newTestLogger 创建写到给定目标的日志器。
func newTestLogger(target io.Writer) *slog.Logger {
	return slog.New(NewLogHandler(target, slog.LevelDebug))
}
