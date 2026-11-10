package watchlist

import (
	"log/slog"
	"strings"
	"testing"
)

// slogLogger 是测试用日志器别名，避免测试文件里到处写 slog.New。
type slogLogger = slog.Logger

// newSlogLogger 把插件日志写进 t.Log，失败时能看到真实上下文。
func newSlogLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
