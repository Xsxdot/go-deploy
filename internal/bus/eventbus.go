package bus

import (
	"log/slog"
	"sync"

	"github.com/Xsxdot/go-deploy/internal/core"
)

// EventHandler 消费者接口 (TUI 或 FileLogger 都需要实现这个接口)
type EventHandler interface {
	HandleEvent(e core.Event)
	Close() error
}

// EventBus 事件总线，接收 Event 并扇出给所有注册的 Handler
type EventBus struct {
	stream   chan core.Event
	handlers []EventHandler
	mu       sync.RWMutex // 保护 handlers 的并发访问
	wg       sync.WaitGroup
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewEventBus 初始化总线，给定一个缓冲大小 (如 5000)
func NewEventBus(bufferSize int) *EventBus {
	return &EventBus{
		stream: make(chan core.Event, bufferSize),
		stopCh: make(chan struct{}),
	}
}

// Register 注册消费者（线程安全）
func (b *EventBus) Register(h EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, h)
}

// Start 启动后台分发协程
func (b *EventBus) Start() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			select {
			case event := <-b.stream:
				b.dispatch(event)
			case <-b.stopCh:
				// Drain 残留事件：使用 select+default 非阻塞读取，避免 len+<- 之间的竞态
				for {
					select {
					case event := <-b.stream:
						b.dispatch(event)
					default:
						return
					}
				}
			}
		}
	}()
}

// dispatch 将事件分发给所有 handler，单个 handler panic 不会影响其他 handler 或 EventBus
func (b *EventBus) dispatch(event core.Event) {
	b.mu.RLock()
	handlers := make([]EventHandler, len(b.handlers))
	copy(handlers, b.handlers)
	b.mu.RUnlock()

	for _, handler := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("事件处理器 panic 已恢复", "recovery", r, "event_type", event.Type, "event_message", event.Message)
				}
			}()
			handler.HandleEvent(event)
		}()
	}
}

// Publish 发布事件到总线。
// 设计理念：如果消费者太慢导致 buffer 满了，宁可阻塞生产者，也不能丢日志（正常运行期间）。
// Stop() 调用后 stopCh 关闭，此时若仍调用 Publish 则不再阻塞，事件丢弃，以避免死锁。
func (b *EventBus) Publish(e core.Event) {
	select {
	case b.stream <- e:
		// 已成功发送
	case <-b.stopCh:
		// 已停止，为避免死锁（无消费者读取 stream）不再阻塞，事件丢弃
	}
}

// Stop 优雅关闭总线
func (b *EventBus) Stop() {
	b.stopOnce.Do(func() {
		close(b.stopCh)
	})
	b.wg.Wait()
	b.mu.Lock()
	handlers := make([]EventHandler, len(b.handlers))
	copy(handlers, b.handlers)
	b.mu.Unlock()
	for _, handler := range handlers {
		_ = handler.Close()
	}
}
