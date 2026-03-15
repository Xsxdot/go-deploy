package tui

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xsxdot/go-deploy/internal/bus"
	"github.com/Xsxdot/go-deploy/internal/core"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	batchInterval = 100 * time.Millisecond // 10 FPS
	bufferSize    = 5000
)

// batchEventMsg 批量事件消息，供 Bubble Tea Update 消费
type batchEventMsg struct {
	Events []core.Event
}

// EventHandler 实现 bus.EventHandler，将 EventBus 事件批处理并桥接到 tea.Msg 通道
type EventHandler struct {
	mu           sync.Mutex
	buffer       []core.Event
	sendCh       chan<- tea.Msg
	stopCh       chan struct{}
	stopOnce     sync.Once
	drainWg      sync.WaitGroup
	closeSendCh  bool // 是否在 Close 时关闭 sendCh
	droppedCount atomic.Int64
}

// NewEventHandler 创建 TUI 事件处理器。sendCh 用于向 tea.Program 发送消息。
func NewEventHandler(sendCh chan<- tea.Msg) *EventHandler {
	h := &EventHandler{
		buffer:      make([]core.Event, 0, 256),
		sendCh:      sendCh,
		stopCh:      make(chan struct{}),
		closeSendCh: true,
	}
	h.drainWg.Add(1)
	go h.drainLoop()
	return h
}

// HandleEvent 实现 bus.EventHandler，将事件追加到缓冲区
func (h *EventHandler) HandleEvent(e core.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.buffer) < bufferSize {
		h.buffer = append(h.buffer, e)
	} else {
		h.droppedCount.Add(1)
	}
}

// drainLoop 每 100ms 将缓冲事件打包发送，实现 10 FPS 防抖
func (h *EventHandler) drainLoop() {
	defer h.drainWg.Done()
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopCh:
			h.flush()
			return
		case <-ticker.C:
			h.flush()
		}
	}
}

func (h *EventHandler) flush() {
	h.mu.Lock()
	if len(h.buffer) == 0 {
		h.mu.Unlock()
		return
	}
	events := make([]core.Event, len(h.buffer))
	copy(events, h.buffer)
	h.buffer = h.buffer[:0]
	h.mu.Unlock()

	select {
	case h.sendCh <- batchEventMsg{Events: events}:
	default:
		// 非阻塞发送，避免阻塞 EventBus
	}
}

// Close 实现 bus.EventHandler，停止 drain goroutine
func (h *EventHandler) Close() error {
	h.stopOnce.Do(func() {
		close(h.stopCh)
		h.drainWg.Wait() // 等待 drainLoop 完全退出后再关闭 sendCh
		if n := h.droppedCount.Load(); n > 0 {
			slog.Warn("事件缓冲区溢出，丢弃事件", "count", n)
		}
		if h.closeSendCh && h.sendCh != nil {
			close(h.sendCh)
		}
	})
	return nil
}

// Ensure EventHandler implements bus.EventHandler
var _ bus.EventHandler = (*EventHandler)(nil)
