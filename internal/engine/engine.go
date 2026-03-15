package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Knetic/govaluate"
	"github.com/Xsxdot/go-deploy/internal/bus"
	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
	"github.com/Xsxdot/go-deploy/pkg/tmpl"

	"golang.org/x/sync/errgroup"
)

// eventBusLogger 将 sshutil.Logger 桥接到 EventBus，供 TUI 显示
type eventBusLogger struct {
	bus core.EventPublisher
}

func (e *eventBusLogger) Error(msg string, args ...any) {
	if e.bus == nil {
		return
	}
	detail := msg
	if len(args) > 0 {
		detail = msg + " " + fmt.Sprint(args...)
	}
	e.bus.Publish(core.Event{
		Timestamp: time.Now(),
		Type:      core.EventLog,
		Level:     "ERROR",
		Message:   detail,
	})
}

// stepState 用于在 Goroutine 之间传递步骤的执行状态
type stepState struct {
	err  error         // 步骤的执行结果
	done chan struct{} // 当步骤执行完毕（无论成功失败）时，关闭此 channel 广播给所有等待者
}

// DeploymentMeta 部署元数据，供 apply 命令传入，Engine 结束时发布 Pipeline 完成事件
type DeploymentMeta struct {
	ProjectName string
	Version     string
	Message     string
	EnvName     string // 环境标识，用于 HandleEvent 更新 deployments
}

// RunOpts 可选的 Run 参数，用于 WorkspaceDir 与审计闭环
type RunOpts struct {
	WorkspaceDir   string
	DeploymentMeta *DeploymentMeta
	// DryRun 为 true 时跳过真实 SSH 调用，通过 EventBus 打印拟执行内容，不产生新版本
	DryRun bool
	// Env 目标环境，用于变量合并与 run_if
	Env string
	// CLIVars 命令行注入变量，优先级最高
	CLIVars map[string]string
	// SkipBuild 晋升/回滚时为 true，跳过 Build 阶段
	SkipBuild bool
	// FromEnv 晋升来源环境（与 FromVersion 配合使用）
	FromEnv string
	// FromVersion 晋升/回滚的源版本号，非空时触发历史 params_snapshot 继承
	FromVersion string
	// GetParamsSnapshot 晋升/回滚时获取历史快照，签名为 (env, version) -> JSON string
	GetParamsSnapshot func(env, version string) (string, error)
	// SaveParamsSnapshot 变量合并完成后、执行前调用的回调，用于落库 params_snapshot
	SaveParamsSnapshot func(snapshot string) error
	// AutoConfirm 为 true 时跳过兼容模式的交互确认（如 --yes）
	AutoConfirm bool
	// ApprovalInputChan TUI 模式下传入，manual_approval 从该 channel 读取 y/n；为 nil 时使用 stdin
	ApprovalInputChan <-chan string
}

// Engine 调度引擎，负责解析 Pipeline、匹配 Plugin，并处理高并发投递下的错误边界
type Engine struct {
	mu      sync.RWMutex
	plugins map[string]core.StepPlugin
}

// NewEngine 创建新的调度引擎
func NewEngine() *Engine {
	return &Engine{
		plugins: make(map[string]core.StepPlugin),
	}
}

// Register 注册插件，同名会覆盖
func (e *Engine) Register(p core.StepPlugin) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.plugins[p.Name()] = p
}

// ensureResourceIDs 将 Resources map 的 key 写入各 Target 的 ResourceID，确保 ID()、MarkDead、FilterHealthy 行为一致
func ensureResourceIDs(infra *core.InfraConfig) {
	if infra == nil || infra.Resources == nil {
		return
	}
	for resID, t := range infra.Resources {
		if t == nil {
			continue
		}
		switch h := t.(type) {
		case *core.HostTarget:
			if h != nil && h.ResourceID == "" {
				h.ResourceID = resID
			}
		case *core.K8sTarget:
			if h != nil && h.ResourceID == "" {
				h.ResourceID = resID
			}
		}
	}
}

// ResolveTargets 根据 Roles 解析出通用的 Target 接口列表
func ResolveTargets(targetRoles []string, infra *core.InfraConfig) ([]core.Target, error) {
	if len(targetRoles) == 0 {
		return nil, nil
	}
	if infra.Resources == nil {
		return nil, nil
	}

	uniqueTargets := make(map[string]core.Target)

	for _, roleName := range targetRoles {
		resourceIDs, exists := infra.Roles[roleName]
		if !exists {
			return nil, fmt.Errorf("role '%s' not found", roleName)
		}

		for _, resID := range resourceIDs {
			target, exists := infra.Resources[resID]
			if !exists {
				return nil, fmt.Errorf("resource ID '%s' not found", resID)
			}
			uniqueTargets[resID] = target
		}
	}

	result := make([]core.Target, 0, len(uniqueTargets))
	for _, t := range uniqueTargets {
		result = append(result, t)
	}
	return result, nil
}

// plugin 线程安全获取插件
func (e *Engine) plugin(typ string) (core.StepPlugin, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.plugins[typ]
	return p, ok
}

// evalRunIf 求值 run_if 表达式，返回 (结果为true则执行, 是否有条件)
// 表达式解析失败时返回 (true, true) 即执行，避免误跳过
// params: 变量表，将 ${var} 替换为变量的实际值后求值，避免 govaluate 对 nil 解引用
func evalRunIf(expr string, params map[string]interface{}) (ok bool, hadCondition bool) {
	expr = trimSpace(expr)
	if expr == "" {
		return true, false
	}
	getVal := func(varName string) string {
		if params == nil {
			return ""
		}
		if v, ok := params[varName]; ok && v != nil {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
	// 1. 先替换 "''${var}" 形式（变量在引号内），整体替换为 "value"
	exprForEval := runIfQuotedRef.ReplaceAllStringFunc(expr, func(match string) string {
		sub := runIfQuotedRef.FindStringSubmatch(match)
		if len(sub) >= 2 {
			return strconv.Quote(getVal(sub[1]))
		}
		return match
	})
	// 2. 再替换 standalone ''${var}'' 形式，替换为 "value"
	exprForEval = runIfVarRef.ReplaceAllStringFunc(exprForEval, func(match string) string {
		sub := runIfVarRef.FindStringSubmatch(match)
		if len(sub) >= 2 {
			return strconv.Quote(getVal(sub[1]))
		}
		return match
	})
	e, err := govaluate.NewEvaluableExpression(exprForEval)
	if err != nil {
		return true, true
	}
	result, err := e.Evaluate(nil)
	if err != nil {
		return true, true
	}
	if b, ok := result.(bool); ok {
		return b, true
	}
	return true, true
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

var (
	runIfQuotedRef = regexp.MustCompile(`"\$\{([a-zA-Z0-9_]+)\}"`) // "''${var}" 形式
	runIfVarRef    = regexp.MustCompile(`\$\{([a-zA-Z0-9_]+)\}`)   // standalone ''${var}''
)

// extractVarRefs 从 run_if 表达式中提取 ${varName} 的变量名，供渲染前补全空值
func extractVarRefs(expr string) []string {
	matches := runIfVarRef.FindAllStringSubmatch(expr, -1)
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		if len(m) >= 2 && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// renderStep 创建 step 的渲染副本，对 With、Needs、Roles 中的 ${var} 做变量替换
func renderStep(step core.Step, vars map[string]string) core.Step {
	rendered := core.Step{
		Name:             step.Name,
		Type:             step.Type,
		Needs:            make([]string, len(step.Needs)),
		Roles:            make([]string, len(step.Roles)),
		BatchSize:        step.BatchSize,
		Retries:          step.Retries,
		RetryDelay:       step.RetryDelay,
		TolerateFailures: step.TolerateFailures,
	}
	for i, s := range step.Needs {
		rendered.Needs[i] = tmpl.Render(s, vars)
	}
	for i, s := range step.Roles {
		rendered.Roles[i] = tmpl.Render(s, vars)
	}
	if step.With != nil {
		if v := tmpl.RenderValue(step.With, vars); v != nil {
			if m, ok := v.(map[string]interface{}); ok {
				rendered.With = m
			}
		}
	}
	return rendered
}

// Run 执行 Pipeline，支持 DAG 依赖与 Fail-Fast。
// eventBus 可选，为 nil 时 DeployContext 的 LogInfo/LogWarn/LogError 为 no-op。
// opts 可选，用于 WorkspaceDir 与 DeploymentMeta（审计闭环）。
func (e *Engine) Run(ctx context.Context, pipeline *core.Pipeline, infra *core.InfraConfig, pipelineConfig *core.PipelineConfig, eventBus *bus.EventBus, opts ...RunOpts) error {
	var opt RunOpts
	if len(opts) > 0 {
		opt = opts[0]
	}
	runStart := time.Now()

	// 1. 预检：拓扑排序，检测循环依赖
	order, buildCount, err := ValidateDAG(pipeline)
	if err != nil {
		return err
	}

	// 1.1 兼容性降级：仅有 steps 且 SkipBuild 时需确认
	if len(pipeline.Build) == 0 && len(pipeline.Deploy) == 0 && len(pipeline.Steps) > 0 && opt.SkipBuild {
		if eventBus != nil {
			eventBus.Publish(core.Event{
				Timestamp: time.Now(),
				Type:      core.EventLog,
				Level:     "WARN",
				Message:   "未检测到 build/deploy 阶段切分，正在以旧版兼容模式运行。Steps 将作为唯一执行内容。",
			})
		}
		if !opt.AutoConfirm {
			// TODO: 交互式确认 - 暂用 log 代替，生产可接 bufio.Scanner
			// if !Confirm("将用历史快照执行 steps，未执行构建。是否继续?") { return ErrAborted }
		}
	}

	// 1.5 核心增强：为本次运行创建一个绝对隔离的临时沙箱目录，供 YAML 中步骤使用
	// 每次 Run 获得唯一路径，并发部署互不踩踏；defer 保证无论成功/失败/Panic 都会清理
	runTempDir, err := os.MkdirTemp("", fmt.Sprintf("deploy-run-%s-*", pipeline.Name))
	if err != nil {
		return fmt.Errorf("failed to create run temp dir: %w", err)
	}
	defer os.RemoveAll(runTempDir)

	// 2. 创建全局 SSH Executor，整个 Pipeline 期间复用连接池
	// DryRun 模式下注入 DryRunExecutor，不建立真实 SSH 连接
	var sshExec core.SSHExecutor
	if opt.DryRun {
		sshExec = NewDryRunExecutor(eventBus)
		if eventBus != nil {
			eventBus.Publish(core.Event{
				Timestamp: time.Now(),
				Type:      core.EventLog,
				Level:     "INFO",
				Message:   "[DRY-RUN] 预检模式已开启，所有 SSH 操作将被跳过",
			})
		}
	} else {
		sshOpts := &sshutil.Options{
			// 注入感知 infra 的 DialFunc，支持 Bastion/ProxyJump
			DialFunc: sshutil.NewDialFunc(infra),
		}
		if eventBus != nil {
			sshOpts.Logger = &eventBusLogger{bus: eventBus}
		}
		realExec := sshutil.New(sshOpts)
		defer realExec.Close()
		sshExec = realExec
	}

	// 4. 按阶段切分步骤：Build → Deploy → Steps 顺序执行，每阶段完成后再进入下一阶段
	buildOrder := order[0:buildCount]
	deployOrder := order[buildCount : buildCount+len(pipeline.Deploy)]
	stepsOrder := order[buildCount+len(pipeline.Deploy):]
	phases := [][]*core.Step{buildOrder, deployOrder, stepsOrder}
	if opt.SkipBuild && buildCount > 0 {
		phases = phases[1:]
	}
	// stepsToRun 用于回滚时逆序遍历
	stepsToRun := make([]*core.Step, 0, len(order))
	for _, p := range phases {
		stepsToRun = append(stepsToRun, p...)
	}

	// 5. 构建变量表用于 step 渲染，按 PRD 优先级：globalVars < variables < envVars < version/env < 历史快照 < CLIVars
	var globalVars, variables map[string]string
	if infra != nil {
		globalVars = infra.GlobalVars
	}
	if pipelineConfig != nil {
		variables = pipelineConfig.Variables
	}
	vars := tmpl.BuildVars(globalVars, variables)
	if pipelineConfig != nil && pipelineConfig.Environments != nil && opt.Env != "" {
		if envCfg, ok := pipelineConfig.Environments[opt.Env]; ok && envCfg.Variables != nil {
			for k, v := range envCfg.Variables {
				vars[k] = tmpl.Render(v, vars)
			}
		}
	}
	if opt.DeploymentMeta != nil && opt.DeploymentMeta.Version != "" {
		vars["version"] = opt.DeploymentMeta.Version
	} else {
		vars["version"] = "v" + time.Now().Format("20060102_150405")
	}
	if opt.Env != "" {
		vars["env"] = opt.Env
	} else {
		vars["env"] = "default"
	}
	vars["run_temp_dir"] = runTempDir // 沙箱目录，供 YAML 步骤引用，避免硬编码路径与并发冲突
	// FromVersion 非空时：历史 params_snapshot 覆盖（继承制品标识等）
	// 晋升场景：fromEnv != opt.Env 时，目标环境的环境变量（target_compute、domain 等）不得被来源快照覆盖
	targetEnvKeys := make(map[string]struct{})
	if pipelineConfig != nil && pipelineConfig.Environments != nil && opt.Env != "" {
		if envCfg, ok := pipelineConfig.Environments[opt.Env]; ok && envCfg.Variables != nil {
			for k := range envCfg.Variables {
				targetEnvKeys[k] = struct{}{}
			}
		}
	}
	if opt.FromVersion != "" && opt.GetParamsSnapshot != nil {
		fromEnv := opt.FromEnv
		if fromEnv == "" {
			fromEnv = opt.Env
		}
		snap, err := opt.GetParamsSnapshot(fromEnv, opt.FromVersion)
		if err != nil {
			return fmt.Errorf("get params snapshot from %s@%s: %w", fromEnv, opt.FromVersion, err)
		}
		if snap != "" && snap != "{}" {
			var history map[string]string
			if err := json.Unmarshal([]byte(snap), &history); err != nil {
				return fmt.Errorf("parse params snapshot: %w", err)
			}
			for k, v := range history {
				if fromEnv != opt.Env && len(targetEnvKeys) > 0 {
					if _, isTargetEnvVar := targetEnvKeys[k]; isTargetEnvVar {
						continue // 晋升时跳过：目标环境变量不被来源快照覆盖
					}
				}
				vars[k] = v
			}
		}
	}
	if opt.CLIVars != nil {
		for k, v := range opt.CLIVars {
			vars[k] = v
		}
	}
	// 变量合并完成后、执行前落库 params_snapshot
	if opt.SaveParamsSnapshot != nil && !opt.DryRun {
		snap, _ := json.Marshal(vars)
		if err := opt.SaveParamsSnapshot(string(snap)); err != nil {
			slog.Warn("保存 params_snapshot 失败", "err", err)
		}
	}

	// 6. 确保各 Target 的 ResourceID 与 Resources map key 一致
	ensureResourceIDs(infra)

	// 6.5 构建 nameToStep 供回滚时查找（stepsToRun 内所有步骤）
	nameToStep := make(map[string]*core.Step)
	for _, sp := range stepsToRun {
		s := *sp
		if s.Roles == nil && pipelineConfig != nil && len(pipelineConfig.Roles) > 0 {
			s.Roles = pipelineConfig.Roles
		}
		nameToStep[sp.Name] = &s
	}

	// 6.6 Pipeline 级 EventLog
	if eventBus != nil {
		eventBus.Publish(core.Event{
			Timestamp: time.Now(),
			Type:      core.EventLog,
			Level:     "INFO",
			Message:   fmt.Sprintf("Pipeline validated, starting %d steps", len(stepsToRun)),
		})
	}

	// 7. 按阶段顺序执行：Build → Deploy → Steps，每阶段完成后再进入下一阶段
	outputVars := make(map[string]string)
	outputMu := sync.Mutex{}
	stateMap := make(map[string]*stepState)
	var runErr error
	for _, phaseOrder := range phases {
		if len(phaseOrder) == 0 {
			continue
		}
		eg, egCtx := errgroup.WithContext(ctx)
		deployCtx := core.NewDeployContext(egCtx, infra, pipelineConfig, vars, eventBus, opt.WorkspaceDir)
		deployCtx.SSHExecutor = sshExec
		if opt.ApprovalInputChan != nil {
			deployCtx.ApprovalInputChan = opt.ApprovalInputChan
		}
		deployCtx.SetOutputVar = func(k, v string) {
			vars[k] = v
			outputMu.Lock()
			outputVars[k] = v
			outputMu.Unlock()
		}
		for _, sp := range phaseOrder {
			stateMap[sp.Name] = &stepState{done: make(chan struct{})}
		}
		phaseSteps := make([]core.Step, len(phaseOrder))
		for i := range phaseOrder {
			phaseSteps[i] = *phaseOrder[i]
			if phaseSteps[i].Roles == nil && pipelineConfig != nil && len(pipelineConfig.Roles) > 0 {
				phaseSteps[i].Roles = pipelineConfig.Roles
			}
		}

		for i := range phaseSteps {
			step := phaseSteps[i]
			eg.Go(func() error {
				for _, depName := range step.Needs {
					depState, exists := stateMap[depName]
					if !exists {
						return fmt.Errorf("step '%s' has unknown dependency: '%s'", step.Name, depName)
					}
					select {
					case <-depState.done:
						if depState.err != nil {
							return fmt.Errorf("step '%s' skipped because dependency '%s' failed: %w", step.Name, depName, depState.err)
						}
					case <-egCtx.Done():
						return egCtx.Err()
					}
				}

				plugin, ok := e.plugin(step.Type)
				if !ok {
					return fmt.Errorf("unknown step type: '%s' (step '%s')", step.Type, step.Name)
				}

				if step.RunIf != "" {
					runIfVars := make(map[string]string)
					for k, v := range vars {
						runIfVars[k] = v
					}
					for _, name := range extractVarRefs(step.RunIf) {
						if _, has := runIfVars[name]; !has {
							runIfVars[name] = ""
						}
					}
					runIfParams := make(map[string]interface{}, len(runIfVars))
					for k, v := range runIfVars {
						runIfParams[k] = v
					}
					if ok, skip := evalRunIf(step.RunIf, runIfParams); skip && !ok {
						if eventBus != nil {
							eventBus.Publish(core.Event{Timestamp: time.Now(), Type: core.EventStatus, Level: "INFO", StepName: step.Name, Message: "Skipped by run_if condition"})
						}
						stateMap[step.Name].err = nil
						close(stateMap[step.Name].done)
						return nil
					}
				}

				renderedStep := renderStep(step, vars)
				targets, err := ResolveTargets(renderedStep.Roles, infra)
				if err != nil {
					return err
				}
				if len(renderedStep.Roles) > 0 && len(targets) > 0 {
					targets = deployCtx.FilterHealthy(targets)
				}

				startTime := time.Now()
				if eventBus != nil {
					eventBus.Publish(core.Event{Timestamp: startTime, Type: core.EventStatus, Level: "INFO", StepName: step.Name, Message: "Running"})
				}
				if opt.DryRun && eventBus != nil {
					targetIDs := make([]string, 0, len(targets))
					for _, t := range targets {
						targetIDs = append(targetIDs, t.ID())
					}
					eventBus.Publish(core.Event{
						Timestamp: time.Now(),
						Type:      core.EventLog,
						Level:     "INFO",
						StepName:  step.Name,
						Message:   fmt.Sprintf("[DRY-RUN] step=%q type=%q targets=%v with=%v", step.Name, step.Type, targetIDs, renderedStep.With),
					})
				}

				err = plugin.Execute(deployCtx, renderedStep, targets)

				duration := time.Since(startTime)
				if eventBus != nil {
					msg := "Done"
					if err != nil {
						msg = "Failed"
					}
					eventBus.Publish(core.Event{Timestamp: time.Now(), Type: core.EventStatus, Level: "INFO", StepName: step.Name, Message: msg, Payload: duration})
				}
				stateMap[step.Name].err = err
				close(stateMap[step.Name].done)
				return err
			})
		}

		runErr = eg.Wait()
		if runErr != nil {
			break
		}
	}
	runDuration := time.Since(runStart)
	runDurationMs := runDuration.Milliseconds()

	// 7.5 发布 Pipeline 完成事件（供 SqliteStore 审计闭环），再优雅关闭 EventBus
	// DryRun 模式不写入 SQLite，跳过审计闭环事件
	if eventBus != nil && opt.DeploymentMeta != nil && !opt.DryRun {
		msg := "Pipeline Completed"
		status := "SUCCESS"
		if runErr != nil {
			msg = "Pipeline Failed"
			status = "FAILED"
		}
		eventBus.Publish(core.Event{
			Type:    core.EventStatus,
			Message: msg,
			Payload: map[string]interface{}{
				"ProjectName": opt.DeploymentMeta.ProjectName,
				"Version":     opt.DeploymentMeta.Version,
				"EnvName":     opt.DeploymentMeta.EnvName,
				"Status":      status,
				"Message":     opt.DeploymentMeta.Message,
				"DurationMs":  runDurationMs,
				"Error":       runErr,
				"Outputs":     outputVars,
			},
		})
	}

	if runErr == nil {
		if eventBus != nil {
			eventBus.Stop()
		}
		return nil
	}

	// 8. 熔断流程：按拓扑逆序回滚已成功完成的步骤
	// 使用父 ctx 创建 rollbackCtx，因阶段内 egCtx 可能已取消
	rollbackCtx := core.NewDeployContext(ctx, infra, pipelineConfig, vars, eventBus, opt.WorkspaceDir)
	rollbackCtx.SSHExecutor = sshExec
	if opt.ApprovalInputChan != nil {
		rollbackCtx.ApprovalInputChan = opt.ApprovalInputChan
	}
	succeeded := make(map[string]bool)
	for name, state := range stateMap {
		select {
		case <-state.done:
			if state.err == nil {
				succeeded[name] = true
			}
		default:
		}
	}
	for i := len(stepsToRun) - 1; i >= 0; i-- {
		s := stepsToRun[i]
		if !succeeded[s.Name] {
			continue
		}
		plugin, ok := e.plugin(s.Type)
		if !ok {
			continue
		}
		step := nameToStep[s.Name]
		if step == nil {
			continue
		}
		renderedStep := renderStep(*step, vars)
		if eventBus != nil {
			eventBus.Publish(core.Event{
				Timestamp: time.Now(),
				Type:      core.EventLog,
				Level:     "INFO",
				StepName:  s.Name,
				Message:   fmt.Sprintf("Rolling back [%s] %s", s.Type, s.Name),
			})
		}
		rbErr := plugin.Rollback(rollbackCtx, renderedStep)
		if rbErr != nil {
			if eventBus != nil {
				eventBus.Publish(core.Event{
					Timestamp: time.Now(),
					Type:      core.EventLog,
					Level:     "ERROR",
					StepName:  s.Name,
					Message:   fmt.Sprintf("[%s] Rollback failed: %s", s.Type, rbErr.Error()),
				})
			}
			// 记录回滚错误但不覆盖原始错误
			_ = rbErr
		}
	}
	if eventBus != nil {
		eventBus.Stop()
	}
	return runErr
}

// RunDestroyOpts 可选的 RunDestroy 参数
type RunDestroyOpts struct {
	WorkspaceDir string
	DryRun      bool
	Env         string
	Version     string // 来自部署快照，用于变量渲染
	FullPurge   bool   // 为 true 时删除 DNS 记录及整个应用目录
	CLIVars     map[string]string
}

// RunDestroy 按 DAG 逆序执行各插件的 Uninstall，彻底卸载项目
// 从部署快照读取配置，按拓扑逆序依次调用 plugin.Uninstall
func (e *Engine) RunDestroy(ctx context.Context, pipeline *core.Pipeline, infra *core.InfraConfig, pipelineConfig *core.PipelineConfig, eventBus *bus.EventBus, opts ...RunDestroyOpts) error {
	var opt RunDestroyOpts
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.Env == "" {
		opt.Env = "default"
	}

	order, _, err := ValidateDAG(pipeline)
	if err != nil {
		return err
	}

	runTempDir, err := os.MkdirTemp("", fmt.Sprintf("deploy-destroy-%s-*", pipeline.Name))
	if err != nil {
		return fmt.Errorf("failed to create destroy temp dir: %w", err)
	}
	defer os.RemoveAll(runTempDir)

	var sshExec core.SSHExecutor
	if opt.DryRun {
		sshExec = NewDryRunExecutor(eventBus)
		if eventBus != nil {
			eventBus.Publish(core.Event{
				Timestamp: time.Now(),
				Type:      core.EventLog,
				Level:     "INFO",
				Message:   "[DRY-RUN] 卸载预检模式已开启，所有 SSH 操作将被跳过",
			})
		}
	} else {
		sshOpts := &sshutil.Options{DialFunc: sshutil.NewDialFunc(infra)}
		if eventBus != nil {
			sshOpts.Logger = &eventBusLogger{bus: eventBus}
		}
		realExec := sshutil.New(sshOpts)
		defer realExec.Close()
		sshExec = realExec
	}

	// Destroy 使用 order（Build+Deploy+Steps 合并）逆序执行
	steps := make([]core.Step, len(order))
	for i, sp := range order {
		steps[i] = *sp
		if steps[i].Roles == nil && pipelineConfig != nil && len(pipelineConfig.Roles) > 0 {
			steps[i].Roles = pipelineConfig.Roles
		}
	}
	nameToStep := make(map[string]*core.Step)
	for i := range steps {
		nameToStep[steps[i].Name] = &steps[i]
	}

	var globalVars, variables map[string]string
	if infra != nil {
		globalVars = infra.GlobalVars
	}
	if pipelineConfig != nil {
		variables = pipelineConfig.Variables
	}
	vars := tmpl.BuildVars(globalVars, variables)
	if pipelineConfig != nil && pipelineConfig.Environments != nil && opt.Env != "" {
		if envCfg, ok := pipelineConfig.Environments[opt.Env]; ok && envCfg.Variables != nil {
			for k, v := range envCfg.Variables {
				vars[k] = tmpl.Render(v, vars)
			}
		}
	}
	if opt.Version != "" {
		vars["version"] = opt.Version
	}
	vars["env"] = opt.Env
	vars["run_temp_dir"] = runTempDir
	if opt.CLIVars != nil {
		for k, v := range opt.CLIVars {
			vars[k] = v
		}
	}

	deployCtx := core.NewDeployContext(ctx, infra, pipelineConfig, vars, eventBus, opt.WorkspaceDir)
	deployCtx.SSHExecutor = sshExec
	deployCtx.FullPurge = opt.FullPurge
	ensureResourceIDs(infra)

	if eventBus != nil {
		eventBus.Publish(core.Event{
			Timestamp: time.Now(),
			Type:      core.EventLog,
			Level:     "INFO",
			Message:   fmt.Sprintf("Destroy: reversing %d steps", len(order)),
		})
	}

	for i := len(order) - 1; i >= 0; i-- {
		s := order[i]
		step := nameToStep[s.Name]
		if step == nil {
			continue
		}

		plugin, ok := e.plugin(step.Type)
		if !ok {
			continue
		}

		if step.RunIf != "" {
			runIfVars := make(map[string]string)
			for k, v := range vars {
				runIfVars[k] = v
			}
			for _, name := range extractVarRefs(step.RunIf) {
				if _, has := runIfVars[name]; !has {
					runIfVars[name] = ""
				}
			}
			runIfParams := make(map[string]interface{}, len(runIfVars))
			for k, v := range runIfVars {
				runIfParams[k] = v
			}
			if ok, skip := evalRunIf(step.RunIf, runIfParams); skip && !ok {
				if eventBus != nil {
					eventBus.Publish(core.Event{
						Timestamp: time.Now(),
						Type:      core.EventLog,
						Level:     "INFO",
						StepName:  step.Name,
						Message:   "Skipped by run_if condition",
					})
				}
				continue
			}
		}

		renderedStep := renderStep(*step, vars)
		targets, err := ResolveTargets(renderedStep.Roles, infra)
		if err != nil {
			return err
		}
		if len(renderedStep.Roles) > 0 && len(targets) > 0 {
			targets = deployCtx.FilterHealthy(targets)
		}

		destroyStartTime := time.Now()
		if eventBus != nil {
			eventBus.Publish(core.Event{
				Timestamp: destroyStartTime,
				Type:      core.EventStatus,
				Level:     "INFO",
				StepName:  step.Name,
				Message:   "Uninstalling",
			})
		}

		if opt.DryRun && eventBus != nil {
			targetIDs := make([]string, 0, len(targets))
			for _, t := range targets {
				targetIDs = append(targetIDs, t.ID())
			}
			eventBus.Publish(core.Event{
				Timestamp: time.Now(),
				Type:      core.EventLog,
				Level:     "INFO",
				StepName:  step.Name,
				Message:   fmt.Sprintf("[DRY-RUN] Uninstall step=%q type=%q targets=%v", step.Name, step.Type, targetIDs),
			})
		}

		err = plugin.Uninstall(deployCtx, renderedStep, targets)
		if err != nil {
			if eventBus != nil {
				eventBus.Publish(core.Event{
					Timestamp: time.Now(),
					Type:      core.EventLog,
					Level:     "ERROR",
					StepName:  step.Name,
					Message:   "Uninstall failed: " + err.Error(),
				})
				eventBus.Stop()
			}
			return fmt.Errorf("uninstall step %q: %w", step.Name, err)
		}

		if eventBus != nil {
			eventBus.Publish(core.Event{
				Timestamp: time.Now(),
				Type:      core.EventStatus,
				Level:     "INFO",
				StepName:  step.Name,
				Message:   "Done",
				Payload:   time.Since(destroyStartTime),
			})
		}
	}

	if eventBus != nil {
		eventBus.Publish(core.Event{
			Timestamp: time.Now(),
			Type:      core.EventLog,
			Level:     "INFO",
			Message:   "Destroy completed",
		})
		eventBus.Stop()
	}
	return nil
}
