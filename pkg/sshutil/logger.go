package sshutil

// Logger 供 sshutil 在 TUI 模式下通过注入将错误输出到 EventBus，避免直接写 stderr
type Logger interface {
	Error(msg string, args ...any)
}

// nopLogger 默认无操作实现
type nopLogger struct{}

func (nopLogger) Error(string, ...any) {}

// NopLogger 返回无操作 Logger，供默认使用
func NopLogger() Logger { return nopLogger{} }
