package subtitle

import (
	"fmt"
	"log/slog"

	"litepan/internal/logx"
)

// Logger 是字幕模块使用的最小日志接口。
//
// 老版用 helpers.AppLogger.Errorf/Warnf/Infof；LitePan 统一用 log/slog（internal/logx），
// 这里做一层薄适配，使 providers 与 service 不直接依赖 logx，单测可注入空实现。
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// slogLogger 把 Logger 调用转成结构化 slog 记录。
type slogLogger struct{ log *slog.Logger }

// NewLogger 把 logx 的 *slog.Logger 包成字幕模块的 Logger。
// 传 nil 时返回丢弃全部日志的 no-op 实现。
func NewLogger(log *slog.Logger) Logger {
	if log == nil {
		return nopLogger{}
	}
	return &slogLogger{log: log}
}

func (l *slogLogger) Debugf(format string, args ...any) {
	l.log.Debug(fmt.Sprintf(format, args...))
}

func (l *slogLogger) Infof(format string, args ...any) {
	l.log.Info(fmt.Sprintf(format, args...))
}

func (l *slogLogger) Warnf(format string, args ...any) {
	l.log.Warn(fmt.Sprintf(format, args...))
}

func (l *slogLogger) Errorf(format string, args ...any) {
	l.log.Error(fmt.Sprintf(format, args...))
}

// nopLogger 丢弃全部日志。
type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}

// LoggerFromManager 从 logx.Manager 取字幕模块日志器。
func LoggerFromManager(m *logx.Manager) Logger {
	if m == nil {
		return nopLogger{}
	}
	return NewLogger(m.For(logx.ModuleSystem))
}
