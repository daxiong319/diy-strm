// Package dutil 为发现板块移植提供对老 diy-strm helpers/models 的轻量适配，
// 使 tmdb/embycheck 等包几乎原样复用，仅改 import 路径。
package dutil

import (
	"context"
	"fmt"
	"log/slog"
)

// QLogger 兼容老 diy-strm/helpers.QLogger 的最小日志接口（Infof/Debugf/Errorf/Warnf）。
// 底层落到 LitePan 的 slog，格式串用 fmt.Sprintf 展开。
type QLogger struct {
	Log  *slog.Logger
	Name string
}

func (q *QLogger) base() *slog.Logger {
	if q != nil && q.Log != nil {
		return q.Log
	}
	return slog.Default()
}

func (q *QLogger) logf(level slog.Level, format string, args ...any) {
	q.base().Log(context.Background(), level, fmt.Sprintf(format, args...))
}

func (q *QLogger) Infof(format string, args ...any)    { q.logf(slog.LevelInfo, format, args...) }
func (q *QLogger) Debugf(format string, args ...any)   { q.logf(slog.LevelDebug, format, args...) }
func (q *QLogger) Errorf(format string, args ...any)   { q.logf(slog.LevelError, format, args...) }
func (q *QLogger) Warnf(format string, args ...any)    { q.logf(slog.LevelWarn, format, args...) }
func (q *QLogger) RequiredWarnf(format string, args ...any) {
	q.logf(slog.LevelWarn, format, args...)
}
func (q *QLogger) SensitiveDebugf(format string, args ...any) {
	q.logf(slog.LevelDebug, format, args...)
}

// 非格式化变体（对齐老 QLogger 的 Info/Error/Warn/Debug，接受 ...any 直接拼接）。
func (q *QLogger) logmsg(level slog.Level, args ...any) {
	q.base().Log(context.Background(), level, fmt.Sprint(args...))
}

func (q *QLogger) Info(args ...any)    { q.logmsg(slog.LevelInfo, args...) }
func (q *QLogger) Debug(args ...any)   { q.logmsg(slog.LevelDebug, args...) }
func (q *QLogger) Error(args ...any)   { q.logmsg(slog.LevelError, args...) }
func (q *QLogger) Warn(args ...any)    { q.logmsg(slog.LevelWarn, args...) }

// 包级默认 logger，模拟老代码 helpers.TMDBLog / helpers.AppLogger 的全局变量形态，
// 让复制过来的包在 init/包级引用时不至于拿到 nil（真正的初始化在装配层覆盖）。
var (
	// TMDBLog 对应老 helpers.TMDBLog
	TMDBLog = &QLogger{Log: slog.Default(), Name: "tmdb"}
	// AppLogger 对应老 helpers.AppLogger
	AppLogger = &QLogger{Log: slog.Default(), Name: "app"}
)

// DEFAULTUA 对应老 v115open.DEFAULTUA（tmdb 等包做请求时使用的 User-Agent）。
var DEFAULTUA = "litepan-GoClient/discover"
