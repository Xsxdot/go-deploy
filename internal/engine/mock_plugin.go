package engine

import (
	"errors"
	"sync"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
)

// MockPlugin 模拟插件，用于测试和演示调度引擎的并发、时序和容错逻辑
type MockPlugin struct {
	typ string

	// 可配置行为
	ShouldFail bool          // 执行时返回错误
	Delay      time.Duration // 模拟耗时

	// 用于断言的可观测状态
	mu               sync.Mutex
	ExecutedAt       time.Time
	StepNames        []string
	UninstallStepNames []string // RunDestroy 时记录的 Uninstall 调用顺序
	LastWith         map[string]interface{} // 最后一次执行的 step.With（用于验证变量渲染）
}

// NewMockPlugin 创建一个 Mock 插件
func NewMockPlugin(typ string) *MockPlugin {
	return &MockPlugin{typ: typ}
}

// WithFail 配置为执行失败
func (m *MockPlugin) WithFail() *MockPlugin {
	m.ShouldFail = true
	return m
}

// WithDelay 配置执行延迟
func (m *MockPlugin) WithDelay(d time.Duration) *MockPlugin {
	m.Delay = d
	return m
}

// Name 实现 StepPlugin
func (m *MockPlugin) Name() string {
	return m.typ
}

// Execute 实现 StepPlugin
func (m *MockPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	if m.Delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.Delay):
		}
	}

	m.mu.Lock()
	m.ExecutedAt = time.Now()
	m.StepNames = append(m.StepNames, step.Name)
	m.LastWith = step.With // 用于变量渲染测试断言
	m.mu.Unlock()

	if m.ShouldFail {
		return errors.New("mock plugin failed")
	}
	return nil
}

// Rollback 实现 StepPlugin
func (m *MockPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	return nil
}

// Uninstall 实现 StepPlugin
func (m *MockPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	m.mu.Lock()
	m.UninstallStepNames = append(m.UninstallStepNames, step.Name)
	m.mu.Unlock()
	return nil
}

// GetExecutedOrder 返回执行顺序（用于断言）
func (m *MockPlugin) GetExecutedOrder() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.StepNames))
	copy(result, m.StepNames)
	return result
}

// GetUninstalledOrder 返回 Uninstall 调用顺序（用于 RunDestroy 测试）
func (m *MockPlugin) GetUninstalledOrder() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.UninstallStepNames))
	copy(result, m.UninstallStepNames)
	return result
}
