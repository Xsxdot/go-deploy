package core

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Xsxdot/go-deploy/pkg/tmpl"
)

// EventPublisher 事件发布接口，避免 core 包依赖 bus 包产生循环依赖
type EventPublisher interface {
	Publish(e Event)
}

// SSHExecutor 提供在 Target 上执行 SSH 命令的能力，由 pkg/sshutil.Executor 实现
type SSHExecutor interface {
	Run(ctx context.Context, target Target, cmd string, opts interface{}) (stdout, stderr string, code int, err error)
	PutFile(ctx context.Context, target Target, remotePath string, content []byte) error
	PutStream(ctx context.Context, target Target, remotePath string, content io.Reader) error
}

// DeployContext 贯穿全局的执行上下文，控制超时和取消，管理连接池和状态
type DeployContext struct {
	context.Context
	Cancel context.CancelFunc

	// 配置引用
	Infra    *InfraConfig
	Pipeline *PipelineConfig

	// 动态变量渲染引擎，用于 "${var}" 模板替换
	Render func(template string) string

	// RollbackState 供插件在 Execute 时写入回滚所需数据，Rollback 时读取
	RollbackState map[string]interface{}
	rollbackMu    sync.RWMutex

	// stepState 细粒度状态存储，按 (stepName, targetID, key) 三级索引
	// 供 SetStepState/GetStepState 使用，实现精准状态回滚
	stepState   map[string]map[string]map[string]string
	stepStateMu sync.RWMutex

	// deadTargets 已阵亡节点黑名单，供 RunParallel 容错与 FilterHealthy 剔除
	deadTargets map[string]bool
	deadMu      sync.RWMutex

	// SSHExecutor 全局 SSH 执行器，Engine 注入；插件优先使用此复用连接，否则回退为自建
	SSHExecutor SSHExecutor

	// SetOutputVar 由引擎注入，插件成功执行后调用以注入变量到共享 vars 表，供下游步骤使用
	SetOutputVar func(key, value string)

	// Bus 事件总线，供插件发布日志与状态事件；为 nil 时 LogInfo/LogWarn/LogError 为 no-op
	Bus EventPublisher

	// WorkspaceDir 封印时的绝对工作目录，供 ResolvePath 解析相对路径；空时按当前目录
	WorkspaceDir string

	// ApprovalInputChan 可选；TUI 模式下由 Engine 注入，manual_approval 从该 channel 读取 y/n 而非 stdin
	// 为 nil 时 manual_approval 回退到 os.Stdin（非 TUI 或单测）
	ApprovalInputChan <-chan string

	// FullPurge 为 true 时，destroy 执行彻底卸载：删除 DNS 记录及整个应用目录（含所有版本）
	FullPurge bool
}

// NewDeployContext 创建一个带取消能力的 DeployContext。
// 若 vars 非 nil，则使用该 vars 构建 Render；否则从 infra 与 pipelineConfig 构建 vars。
// bus 可选，为 nil 时 LogInfo/LogWarn/LogError 为 no-op。
// workspaceDir 可选，用于硬持久化模式下的路径解析；空字符串时 ResolvePath 退化为基于当前目录。
func NewDeployContext(parent context.Context, infra *InfraConfig, pipeline *PipelineConfig, vars map[string]string, bus EventPublisher, workspaceDir string) *DeployContext {
	ctx, cancel := context.WithCancel(parent)
	if vars == nil {
		var globalVars, variables map[string]string
		if infra != nil {
			globalVars = infra.GlobalVars
		}
		if pipeline != nil {
			variables = pipeline.Variables
		}
		vars = tmpl.BuildVars(globalVars, variables)
	}
	return &DeployContext{
		Context:       ctx,
		Cancel:        cancel,
		Infra:         infra,
		Pipeline:      pipeline,
		Render:        tmpl.NewRenderer(vars),
		RollbackState: make(map[string]interface{}),
		stepState:     make(map[string]map[string]map[string]string),
		Bus:           bus,
		WorkspaceDir:  workspaceDir,
	}
}

// ResolvePath 将相对路径解析为绝对路径，统一用 filepath.Clean 防止路径穿越。
// 支持 @/ 前缀表示 workspace 根；若 path 已是绝对路径，则 Clean 后返回；
// 若 WorkspaceDir 为空，则基于当前目录 Abs；否则基于 WorkspaceDir Join。
func (c *DeployContext) ResolvePath(path string) string {
	path = strings.TrimPrefix(path, "@/") // @/ 表示 workspace 根
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if c.WorkspaceDir == "" {
		abs, _ := filepath.Abs(path)
		return filepath.Clean(abs)
	}
	return filepath.Clean(filepath.Join(c.WorkspaceDir, path))
}

// LogInfo 供插件使用的语法糖，发布 INFO 级别日志事件
func (c *DeployContext) LogInfo(stepName, targetID, msg string) {
	if c.Bus != nil {
		c.Bus.Publish(Event{
			Timestamp: time.Now(),
			Type:      EventLog,
			Level:     "INFO",
			StepName:  stepName,
			TargetID:  targetID,
			Message:   msg,
		})
	}
}

// LogWarn 供插件使用的语法糖，发布 WARN 级别日志事件
func (c *DeployContext) LogWarn(stepName, targetID, msg string) {
	if c.Bus != nil {
		c.Bus.Publish(Event{
			Timestamp: time.Now(),
			Type:      EventLog,
			Level:     "WARN",
			StepName:  stepName,
			TargetID:  targetID,
			Message:   msg,
		})
	}
}

// LogError 供插件使用的语法糖，发布 ERROR 级别日志事件
func (c *DeployContext) LogError(stepName, targetID, msg string) {
	if c.Bus != nil {
		c.Bus.Publish(Event{
			Timestamp: time.Now(),
			Type:      EventLog,
			Level:     "ERROR",
			StepName:  stepName,
			TargetID:  targetID,
			Message:   msg,
		})
	}
}

// SetRollbackData 线程安全写入回滚数据
func (c *DeployContext) SetRollbackData(stepName string, data interface{}) {
	c.rollbackMu.Lock()
	defer c.rollbackMu.Unlock()
	if c.RollbackState == nil {
		c.RollbackState = make(map[string]interface{})
	}
	c.RollbackState[stepName] = data
}

// GetRollbackData 线程安全读取回滚数据
func (c *DeployContext) GetRollbackData(stepName string) (interface{}, bool) {
	c.rollbackMu.RLock()
	defer c.rollbackMu.RUnlock()
	v, ok := c.RollbackState[stepName]
	return v, ok
}

// MarkDead 将彻底失败的节点加入黑名单，防止下游步骤继续使用
func (c *DeployContext) MarkDead(targetID string) {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	if c.deadTargets == nil {
		c.deadTargets = make(map[string]bool)
	}
	c.deadTargets[targetID] = true
}

// FilterHealthy 过滤掉已经在上游步骤中阵亡的机器
func (c *DeployContext) FilterHealthy(targets []Target) []Target {
	c.deadMu.RLock()
	defer c.deadMu.RUnlock()
	if len(c.deadTargets) == 0 {
		return targets
	}
	var healthy []Target
	for _, t := range targets {
		if !c.deadTargets[t.ID()] {
			healthy = append(healthy, t)
		}
	}
	return healthy
}

// SetStepState 以 (stepName, targetID, key) 三级索引线程安全写入回滚状态。
// 相比 SetRollbackData，此方法提供更细粒度的 per-target 状态管理，
// 适合插件在 Execute 阶段记录远端当前状态（如旧软链接路径），供 Rollback 精准还原。
func (c *DeployContext) SetStepState(stepName, targetID, key, value string) {
	c.stepStateMu.Lock()
	defer c.stepStateMu.Unlock()
	if c.stepState == nil {
		c.stepState = make(map[string]map[string]map[string]string)
	}
	if c.stepState[stepName] == nil {
		c.stepState[stepName] = make(map[string]map[string]string)
	}
	if c.stepState[stepName][targetID] == nil {
		c.stepState[stepName][targetID] = make(map[string]string)
	}
	c.stepState[stepName][targetID][key] = value
}

// GetStepState 以 (stepName, targetID, key) 三级索引线程安全读取回滚状态。
// 返回值与 ok 语义同 map 访问。
func (c *DeployContext) GetStepState(stepName, targetID, key string) (string, bool) {
	c.stepStateMu.RLock()
	defer c.stepStateMu.RUnlock()
	if c.stepState == nil {
		return "", false
	}
	targets, ok := c.stepState[stepName]
	if !ok {
		return "", false
	}
	keys, ok := targets[targetID]
	if !ok {
		return "", false
	}
	v, ok := keys[key]
	return v, ok
}
