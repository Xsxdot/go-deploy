package core

import (
	"bytes"
	"io"
)

// LineWriter 缓冲字节直到换行，对每一行调用 onLine。
// 供插件把 stdout/stderr 输出转成 EventLog，实现 io.Writer 接口。
type LineWriter struct {
	onLine func(line string)
	buf    []byte
}

// NewLineWriter 创建 LineWriter，onLine 会在每收到完整一行时被调用。
func NewLineWriter(onLine func(line string)) *LineWriter {
	return &LineWriter{
		onLine: onLine,
		buf:    make([]byte, 0, 256),
	}
}

// Write 实现 io.Writer，按行缓冲并回调 onLine
func (w *LineWriter) Write(p []byte) (n int, err error) {
	if w.onLine == nil {
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if line != "" {
			w.onLine(line)
		}
	}
	return len(p), nil
}

// Flush 将缓冲区中剩余内容作为最后一行回调（如有），供命令结束后调用
func (w *LineWriter) Flush() {
	if w.onLine == nil || len(w.buf) == 0 {
		return
	}
	line := string(bytes.TrimRight(w.buf, "\r\n"))
	w.buf = w.buf[:0]
	if line != "" {
		w.onLine(line)
	}
}

// Ensure LineWriter implements io.Writer
var _ io.Writer = (*LineWriter)(nil)
