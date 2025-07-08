package log

import (
	"fmt"
	"time"
)

// Logger 日志工具类
type Logger struct{}

// 定义颜色代码
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"
)

// 创建全局日志实例
var Log = &Logger{}

// Info 打印信息日志
func (l *Logger) Info(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	prefix := fmt.Sprintf("%s[%s INFO]%s", ColorBlue, timestamp, ColorReset)
	message := fmt.Sprintf(format, args...)
	fmt.Printf("%s %s\n", prefix, message)
}

// Success 打印成功日志
func (l *Logger) Success(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	prefix := fmt.Sprintf("%s[%s SUCCESS]%s", ColorGreen, timestamp, ColorReset)
	message := fmt.Sprintf(format, args...)
	fmt.Printf("%s %s%s%s%s\n", prefix, ColorGreen, ColorBold, message, ColorReset)
}

// Warning 打印警告日志
func (l *Logger) Warning(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	prefix := fmt.Sprintf("%s[%s WARNING]%s", ColorYellow, timestamp, ColorReset)
	message := fmt.Sprintf(format, args...)
	fmt.Printf("%s %s%s%s%s\n", prefix, ColorYellow, ColorBold, message, ColorReset)
}

// Error 打印错误日志
func (l *Logger) Error(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	prefix := fmt.Sprintf("%s[%s ERROR]%s", ColorRed, timestamp, ColorReset)
	message := fmt.Sprintf(format, args...)
	fmt.Printf("%s %s%s%s%s\n", prefix, ColorRed, ColorBold, message, ColorReset)
}

// Step 打印步骤日志
func (l *Logger) Step(step int, total int, format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	prefix := fmt.Sprintf("%s[%s STEP %d/%d]%s", ColorCyan, timestamp, step, total, ColorReset)
	message := fmt.Sprintf(format, args...)
	fmt.Printf("\n%s %s%s%s%s\n", prefix, ColorCyan, ColorBold, message, ColorReset)
}

// Command 打印命令执行日志
func (l *Logger) Command(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	prefix := fmt.Sprintf("%s[%s CMD]%s", ColorPurple, timestamp, ColorReset)
	message := fmt.Sprintf(format, args...)
	fmt.Printf("%s %s%s$ %s%s\n", prefix, ColorPurple, ColorBold, message, ColorReset)
}

// Progress 打印进度日志
func (l *Logger) Progress(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	prefix := fmt.Sprintf("%s[%s PROGRESS]%s", ColorCyan, timestamp, ColorReset)
	message := fmt.Sprintf(format, args...)
	fmt.Printf("%s %s\n", prefix, message)
}

// Separator 打印分隔线
func (l *Logger) Separator() {
	fmt.Printf("%s%s===========================================%s\n", ColorBlue, ColorBold, ColorReset)
}
