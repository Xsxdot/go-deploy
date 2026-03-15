package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xsxdot/go-deploy/internal/bus"
	"github.com/Xsxdot/go-deploy/internal/core"
)

// TestEngine_ConcurrentSteps 验证无 Needs 依赖的 Step 是否并发启动
func TestEngine_ConcurrentSteps(t *testing.T) {
	mock := NewMockPlugin("mock").WithDelay(50 * time.Millisecond)
	eng := NewEngine()
	eng.Register(mock)

	pipeline := &core.Pipeline{
		Name: "concurrent",
		Steps: []core.Step{
			{Name: "a", Type: "mock"},
			{Name: "b", Type: "mock"},
			{Name: "c", Type: "mock"},
		},
	}

	start := time.Now()
	err := eng.Run(context.Background(), pipeline, nil, nil, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3 个 50ms 并发执行，总耗时应接近 50ms 而非 150ms
	if elapsed > 100*time.Millisecond {
		t.Errorf("expected concurrent execution ~50ms, got %v", elapsed)
	}
	order := mock.GetExecutedOrder()
	if len(order) != 3 {
		t.Errorf("expected 3 steps executed, got %d", len(order))
	}
}

// TestEngine_DependencyOrder 验证 A 依赖 B 时，A 严格等待 B 完成后才执行
func TestEngine_DependencyOrder(t *testing.T) {
	mock := NewMockPlugin("mock")
	eng := NewEngine()
	eng.Register(mock)

	// B 先执行（无依赖），A 依赖 B
	pipeline := &core.Pipeline{
		Name: "order",
		Steps: []core.Step{
			{Name: "a", Type: "mock", Needs: []string{"b"}},
			{Name: "b", Type: "mock"},
		},
	}

	err := eng.Run(context.Background(), pipeline, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	order := mock.GetExecutedOrder()
	if len(order) != 2 {
		t.Fatalf("expected 2 steps, got %v", order)
	}
	if order[0] != "b" || order[1] != "a" {
		t.Errorf("expected order [b, a], got %v", order)
	}
}

// TestEngine_FailFast 验证某 Step 报错时，后续关联 Step 被阻断
func TestEngine_FailFast(t *testing.T) {
	okPlugin := NewMockPlugin("ok")
	failPlugin := NewMockPlugin("fail").WithFail()

	eng := NewEngine()
	eng.Register(okPlugin)
	eng.Register(failPlugin)

	// fail_step 会报错，downstream 依赖它，不应执行
	pipeline := &core.Pipeline{
		Name: "failfast",
		Steps: []core.Step{
			{Name: "ok_step", Type: "ok"},
			{Name: "fail_step", Type: "fail", Needs: []string{"ok_step"}},
			{Name: "downstream", Type: "ok", Needs: []string{"fail_step"}},
		},
	}

	err := eng.Run(context.Background(), pipeline, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error from fail_step")
	}

	okOrder := okPlugin.GetExecutedOrder()
	// ok_step 和 fail_step 会执行，downstream 不应执行
	if len(okOrder) != 1 || okOrder[0] != "ok_step" {
		t.Errorf("downstream should not run; ok executed: %v", okOrder)
	}
}

// TestEngine_CycleDetection 验证循环依赖在预检阶段被阻断
func TestEngine_CycleDetection(t *testing.T) {
	mock := NewMockPlugin("mock")
	eng := NewEngine()
	eng.Register(mock)

	pipeline := &core.Pipeline{
		Name: "cycle",
		Steps: []core.Step{
			{Name: "a", Type: "mock", Needs: []string{"c"}},
			{Name: "b", Type: "mock", Needs: []string{"a"}},
			{Name: "c", Type: "mock", Needs: []string{"b"}},
		},
	}

	err := eng.Run(context.Background(), pipeline, nil, nil, nil)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if err != nil && !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle-related error, got: %v", err)
	}
}

// TestEngine_UnknownDependency 验证未知依赖报错
func TestEngine_UnknownDependency(t *testing.T) {
	mock := NewMockPlugin("mock")
	eng := NewEngine()
	eng.Register(mock)

	pipeline := &core.Pipeline{
		Name: "unknown",
		Steps: []core.Step{
			{Name: "a", Type: "mock", Needs: []string{"nonexistent"}},
		},
	}

	err := eng.Run(context.Background(), pipeline, nil, nil, nil)
	if err == nil {
		t.Fatal("expected unknown dependency error")
	}
}

// TestEngine_UnknownPluginType 验证未注册的插件类型报错
func TestEngine_UnknownPluginType(t *testing.T) {
	eng := NewEngine()

	pipeline := &core.Pipeline{
		Name: "unknown_plugin",
		Steps: []core.Step{
			{Name: "a", Type: "nonexistent_plugin"},
		},
	}

	err := eng.Run(context.Background(), pipeline, nil, nil, nil)
	if err == nil {
		t.Fatal("expected unknown plugin type error")
	}
}

// rolesCapturePlugin 用于测试 roles 继承，记录收到的 targets
type rolesCapturePlugin struct {
	capturedTargets []core.Target
}

func (p *rolesCapturePlugin) Name() string { return "roles_capture" }
func (p *rolesCapturePlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	p.capturedTargets = make([]core.Target, len(targets))
	copy(p.capturedTargets, targets)
	return nil
}
func (p *rolesCapturePlugin) Rollback(ctx *core.DeployContext, step core.Step) error   { return nil }
func (p *rolesCapturePlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error { return nil }

// TestEngine_RolesInheritance 验证 step 未写 roles 时继承 pipelineConfig.Roles
func TestEngine_RolesInheritance(t *testing.T) {
	cap := &rolesCapturePlugin{}
	eng := NewEngine()
	eng.Register(cap)

	infra := &core.InfraConfig{
		Resources: map[string]core.Target{
			"node-01": &core.HostTarget{ResourceID: "node-01", Addr: "192.168.1.1", User: "root", Auth: map[string]string{"keyPath": "~/.ssh/id_rsa"}},
		},
		Roles: map[string][]string{
			"compute": {"node-01"},
		},
	}
	pipelineConfig := &core.PipelineConfig{
		Roles: []string{"compute"},
		Pipeline: core.Pipeline{
			Name: "roles-inherit",
			Steps: []core.Step{
				{Name: "inherited", Type: "roles_capture"}, // 未写 roles，应继承 [compute]
			},
		},
	}

	err := eng.Run(context.Background(), &pipelineConfig.Pipeline, infra, pipelineConfig, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cap.capturedTargets) != 1 {
		t.Errorf("expected 1 target from inherited roles [compute], got %d", len(cap.capturedTargets))
	}
	if len(cap.capturedTargets) > 0 && cap.capturedTargets[0].ID() != "node-01" {
		t.Errorf("expected target node-01, got %s", cap.capturedTargets[0].ID())
	}
}

// TestEngine_RolesNoInheritWhenExplicitEmpty 验证 step 显式写 roles: [] 时不继承
func TestEngine_RolesNoInheritWhenExplicitEmpty(t *testing.T) {
	cap := &rolesCapturePlugin{}
	eng := NewEngine()
	eng.Register(cap)

	infra := &core.InfraConfig{
		Resources: map[string]core.Target{
			"node-01": &core.HostTarget{ResourceID: "node-01", Addr: "192.168.1.1", User: "root", Auth: map[string]string{}},
		},
		Roles: map[string][]string{"compute": {"node-01"}},
	}
	pipelineConfig := &core.PipelineConfig{
		Roles: []string{"compute"},
		Pipeline: core.Pipeline{
			Name: "roles-no-inherit",
			Steps: []core.Step{
				{Name: "local_only", Type: "roles_capture", Roles: []string{}}, // 显式空，阻断继承
			},
		},
	}

	err := eng.Run(context.Background(), &pipelineConfig.Pipeline, infra, pipelineConfig, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cap.capturedTargets) != 0 {
		t.Errorf("expected 0 targets when roles: [] blocks inheritance, got %d", len(cap.capturedTargets))
	}
}

// varCapturePlugin 用于测试变量渲染，记录收到的 step.With
type varCapturePlugin struct {
	capturedWith map[string]interface{}
}

func (p *varCapturePlugin) Name() string { return "var_capture" }
func (p *varCapturePlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	p.capturedWith = step.With
	return nil
}
func (p *varCapturePlugin) Rollback(ctx *core.DeployContext, step core.Step) error   { return nil }
func (p *varCapturePlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error { return nil }

// TestEngine_VariableRendering 验证 step.With 中的 ${var} 在执行前被正确渲染
func TestEngine_VariableRendering(t *testing.T) {
	cap := &varCapturePlugin{}
	eng := NewEngine()
	eng.Register(cap)

	infra := &core.InfraConfig{
		GlobalVars: map[string]string{
			"baseInstallPath": "/opt/services",
			"deployUser":      "deploy",
		},
	}
	pipelineConfig := &core.PipelineConfig{
		Variables: map[string]string{
			"agentWorkDir": "${baseInstallPath}/video-agent",
		},
		Pipeline: core.Pipeline{
			Name: "var-test",
			Steps: []core.Step{
				{
					Name: "render_step",
					Type: "var_capture",
					With: map[string]interface{}{
						"target": "${agentWorkDir}/current/bin/agent",
						"user":   "${deployUser}",
					},
				},
			},
		},
	}

	err := eng.Run(context.Background(), &pipelineConfig.Pipeline, infra, pipelineConfig, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.capturedWith == nil {
		t.Fatal("plugin did not receive With")
	}
	if g := cap.capturedWith["target"]; g != "/opt/services/video-agent/current/bin/agent" {
		t.Errorf("target: got %q, want /opt/services/video-agent/current/bin/agent", g)
	}
	if g := cap.capturedWith["user"]; g != "deploy" {
		t.Errorf("user: got %q, want deploy", g)
	}
}

// runIfCapturePlugin 记录是否被执行，用于 run_if 验收测试
type runIfCapturePlugin struct {
	executed []string
}

func (p *runIfCapturePlugin) Name() string { return "runif_capture" }
func (p *runIfCapturePlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	p.executed = append(p.executed, step.Name)
	return nil
}
func (p *runIfCapturePlugin) Rollback(ctx *core.DeployContext, step core.Step) error   { return nil }
func (p *runIfCapturePlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error { return nil }

// TestEngine_RunIfSkip 验收 PRD：run_if 为 false 时跳过步骤
func TestEngine_RunIfSkip(t *testing.T) {
	cap := &runIfCapturePlugin{}
	eng := NewEngine()
	eng.Register(cap)

	// build 步骤：run_if="${image_tag} == ''" — 当 image_tag 为空时执行
	// deploy 步骤：无 run_if，始终执行
	pipelineConfig := &core.PipelineConfig{
		Variables: map[string]string{"app": "demo"},
		Pipeline: core.Pipeline{
			Name: "runif-test",
			Steps: []core.Step{
				{
					Name:  "build",
					Type:  "runif_capture",
					RunIf: `"${image_tag}" == ""`,
					With:  map[string]interface{}{"phase": "build"},
				},
				{
					Name:  "deploy",
					Type:  "runif_capture",
					Needs: []string{"build"},
					With:  map[string]interface{}{"phase": "deploy"},
				},
			},
		},
	}

	// 场景1：image_tag 未设置（通过 CLIVars 不传），build 应执行
	opts := RunOpts{
		Env:     "default",
		CLIVars: map[string]string{},
	}
	err := eng.Run(context.Background(), &pipelineConfig.Pipeline, nil, pipelineConfig, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cap.executed) != 2 {
		t.Errorf("no image_tag: expected build+deploy (2 steps), got %v", cap.executed)
	}

	// 场景2：image_tag 已传入，build 应被跳过
	cap.executed = nil
	opts.CLIVars = map[string]string{"image_tag": "v1.0.3"}
	err = eng.Run(context.Background(), &pipelineConfig.Pipeline, nil, pipelineConfig, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cap.executed) != 1 || cap.executed[0] != "deploy" {
		t.Errorf("with image_tag: build should be skipped, only deploy runs; got %v", cap.executed)
	}
}

// eventCaptureHandler 收集 EventLog/EventStatus，用于验收测试断言日志
type eventCaptureHandler struct {
	events []core.Event
	mu     sync.Mutex
}

func (h *eventCaptureHandler) HandleEvent(e core.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
}
func (h *eventCaptureHandler) Close() error { return nil }

func (h *eventCaptureHandler) hasMessage(stepName, msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.events {
		if e.StepName == stepName && e.Message == msg {
			return true
		}
	}
	return false
}

// TestEngine_EnvDynamicRouting 验收 PRD：动态路由解析
// environments 中定义 target_compute，-e test 与 -e prod 下发到不同 IP 组
func TestEngine_EnvDynamicRouting(t *testing.T) {
	cap := &rolesCapturePlugin{}
	eng := NewEngine()
	eng.Register(cap)

	infra := &core.InfraConfig{
		Resources: map[string]core.Target{
			"node-01": &core.HostTarget{ResourceID: "node-01", Addr: "192.168.1.1", User: "root", Auth: map[string]string{}},
			"node-02": &core.HostTarget{ResourceID: "node-02", Addr: "192.168.1.2", User: "root", Auth: map[string]string{}},
		},
		Roles: map[string][]string{
			"engine-test": {"node-01"},
			"engine-prod": {"node-02"},
		},
	}
	pipelineConfig := &core.PipelineConfig{
		Environments: map[string]core.EnvironmentConfig{
			"test": {Variables: map[string]string{"target_compute": "engine-test"}},
			"prod": {Variables: map[string]string{"target_compute": "engine-prod"}},
		},
		Pipeline: core.Pipeline{
			Name: "routing",
			Steps: []core.Step{
				{Name: "deploy", Type: "roles_capture", Roles: []string{"${target_compute}"}},
			},
		},
	}

	// -e test -> node-01
	cap.capturedTargets = nil
	err := eng.Run(context.Background(), &pipelineConfig.Pipeline, infra, pipelineConfig, nil, RunOpts{Env: "test"})
	if err != nil {
		t.Fatalf("run -e test: %v", err)
	}
	if len(cap.capturedTargets) != 1 || cap.capturedTargets[0].ID() != "node-01" {
		t.Errorf("-e test: expected [node-01], got %v", targetIDs(cap.capturedTargets))
	}

	// -e prod -> node-02
	cap.capturedTargets = nil
	err = eng.Run(context.Background(), &pipelineConfig.Pipeline, infra, pipelineConfig, nil, RunOpts{Env: "prod"})
	if err != nil {
		t.Fatalf("run -e prod: %v", err)
	}
	if len(cap.capturedTargets) != 1 || cap.capturedTargets[0].ID() != "node-02" {
		t.Errorf("-e prod: expected [node-02], got %v", targetIDs(cap.capturedTargets))
	}
}

func targetIDs(targets []core.Target) []string {
	ids := make([]string, len(targets))
	for i, t := range targets {
		ids[i] = t.ID()
	}
	return ids
}

// TestEngine_RunIfSkip_EmitsSkippedLogAndImageTag 验收 PRD：条件构建拦截
// apply -e prod --var image_tag=v1.0.3 时输出 Skipped by run_if condition，deploy 步骤正确使用 v1.0.3
func TestEngine_RunIfSkip_EmitsSkippedLogAndImageTag(t *testing.T) {
	cap := &varCapturePlugin{}
	eng := NewEngine()
	eng.Register(cap)

	capture := &eventCaptureHandler{}
	eventBus := bus.NewEventBus(100)
	eventBus.Register(capture)
	eventBus.Start()
	defer eventBus.Stop()

	pipelineConfig := &core.PipelineConfig{
		Variables: map[string]string{"app": "demo"},
		Pipeline: core.Pipeline{
			Name: "runif-log",
			Steps: []core.Step{
				{Name: "Build Artifact", Type: "var_capture", RunIf: `"${image_tag}" == ""`, With: map[string]interface{}{"phase": "build"}},
				{Name: "Deploy Service", Type: "var_capture", Needs: []string{"Build Artifact"}, With: map[string]interface{}{
					"IMAGE_TAG": "${image_tag:-${version}}",
				}},
			},
		},
	}

	opts := RunOpts{
		Env:     "prod",
		CLIVars: map[string]string{"image_tag": "v1.0.3"},
		DeploymentMeta: &DeploymentMeta{Version: "v1.0.2"},
	}
	err := eng.Run(context.Background(), &pipelineConfig.Pipeline, nil, pipelineConfig, eventBus, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !capture.hasMessage("Build Artifact", "Skipped by run_if condition") {
		t.Error("expected EventLog/EventStatus 'Skipped by run_if condition' for Build Artifact")
	}
	if cap.capturedWith == nil {
		t.Fatal("deploy step did not run or did not capture With")
	}
	if g, ok := cap.capturedWith["IMAGE_TAG"].(string); !ok || g != "v1.0.3" {
		t.Errorf("deploy IMAGE_TAG: expected v1.0.3, got %v", cap.capturedWith["IMAGE_TAG"])
	}
}

// TestEngine_RunDestroy 验证 RunDestroy 按 DAG 逆序调用 Uninstall
func TestEngine_RunDestroy(t *testing.T) {
	mock := NewMockPlugin("mock")
	eng := NewEngine()
	eng.Register(mock)

	// DAG: a -> b -> c，拓扑序为 a, b, c；Destroy 逆序应为 c, b, a
	pipeline := &core.Pipeline{
		Name: "destroy-test",
		Steps: []core.Step{
			{Name: "a", Type: "mock"},
			{Name: "b", Type: "mock", Needs: []string{"a"}},
			{Name: "c", Type: "mock", Needs: []string{"b"}},
		},
	}

	eventBus := bus.NewEventBus(100)
	eventBus.Start()

	err := eng.RunDestroy(context.Background(), pipeline, nil, nil, eventBus)
	if err != nil {
		t.Fatalf("RunDestroy: %v", err)
	}

	order := mock.GetUninstalledOrder()
	want := []string{"c", "b", "a"}
	if len(order) != len(want) {
		t.Fatalf("uninstall order length: got %d, want %d; order=%v", len(order), len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("uninstall order[%d]: got %q, want %q; full order=%v", i, order[i], want[i], order)
		}
	}
}

// TestEngine_RunDestroy_DryRun 验证 DryRun 模式下 Uninstall 仍被调用（DryRunExecutor 不执行真实 SSH）
func TestEngine_RunDestroy_DryRun(t *testing.T) {
	mock := NewMockPlugin("mock")
	eng := NewEngine()
	eng.Register(mock)

	pipeline := &core.Pipeline{
		Name: "dryrun-destroy",
		Steps: []core.Step{
			{Name: "step1", Type: "mock"},
		},
	}

	eventBus := bus.NewEventBus(100)
	eventBus.Start()

	err := eng.RunDestroy(context.Background(), pipeline, nil, nil, eventBus, RunDestroyOpts{DryRun: true})
	if err != nil {
		t.Fatalf("RunDestroy dry-run: %v", err)
	}

	order := mock.GetUninstalledOrder()
	if len(order) != 1 || order[0] != "step1" {
		t.Errorf("DryRun should still call Uninstall; got order=%v", order)
	}
}

// TestEngine_DefaultEnvBackwardCompat 验收 PRD：向下兼容性
// 不加 -e 时按 default 环境执行，variables 与 version 正常工作
func TestEngine_DefaultEnvBackwardCompat(t *testing.T) {
	cap := &varCapturePlugin{}
	eng := NewEngine()
	eng.Register(cap)

	pipelineConfig := &core.PipelineConfig{
		Variables: map[string]string{"app": "legacy"},
		Pipeline: core.Pipeline{
			Name: "default-env",
			Steps: []core.Step{
				{Name: "step1", Type: "var_capture", With: map[string]interface{}{
					"app":    "${app}",
					"env":   "${env}",
					"ver":   "${version}",
				}},
			},
		},
	}

	// Env 空字符串视为 default
	err := eng.Run(context.Background(), &pipelineConfig.Pipeline, nil, pipelineConfig, nil, RunOpts{
		Env:            "",
		DeploymentMeta: &DeploymentMeta{Version: "v1.0.0"},
	})
	if err != nil {
		t.Fatalf("run Env empty: %v", err)
	}
	if g := cap.capturedWith["app"]; g != "legacy" {
		t.Errorf("app: got %v, want legacy", g)
	}
	if g := cap.capturedWith["env"]; g != "default" {
		t.Errorf("env: got %v, want default", g)
	}
	if g := cap.capturedWith["ver"]; g != "v1.0.0" {
		t.Errorf("version: got %v, want v1.0.0", g)
	}

	// 显式 Env=default 同理
	cap.capturedWith = nil
	err = eng.Run(context.Background(), &pipelineConfig.Pipeline, nil, pipelineConfig, nil, RunOpts{
		Env:            "default",
		DeploymentMeta: &DeploymentMeta{Version: "v1.0.1"},
	})
	if err != nil {
		t.Fatalf("run Env default: %v", err)
	}
	if g := cap.capturedWith["env"]; g != "default" {
		t.Errorf("env with explicit default: got %v", g)
	}
	if g := cap.capturedWith["ver"]; g != "v1.0.1" {
		t.Errorf("version: got %v, want v1.0.1", g)
	}
}

// TestEngine_LegacyStepsOnly TC-1 兼容性：仅有 steps 的旧 YAML 正常执行
func TestEngine_LegacyStepsOnly(t *testing.T) {
	mock := NewMockPlugin("mock")
	eng := NewEngine()
	eng.Register(mock)

	// 仅 Steps，无 Build/Deploy（旧版 YAML 结构）
	pipeline := &core.Pipeline{
		Name:   "legacy",
		Build:  nil,
		Deploy: nil,
		Steps: []core.Step{
			{Name: "legacy_step", Type: "mock"},
		},
	}

	err := eng.Run(context.Background(), pipeline, nil, nil, nil)
	if err != nil {
		t.Fatalf("legacy steps-only: %v", err)
	}
	order := mock.GetExecutedOrder()
	if len(order) != 1 || order[0] != "legacy_step" {
		t.Errorf("expected [legacy_step], got %v", order)
	}
}

// TestEngine_SkipBuildSkipsBuildPhase TC-2/TC-3：SkipBuild 时跳过 Build 阶段
func TestEngine_SkipBuildSkipsBuildPhase(t *testing.T) {
	mock := NewMockPlugin("mock")
	eng := NewEngine()
	eng.Register(mock)

	pipeline := &core.Pipeline{
		Name: "build-deploy",
		Build: []core.Step{
			{Name: "build_step", Type: "mock"},
		},
		Deploy: []core.Step{
			{Name: "deploy_step", Type: "mock"},
		},
		Steps: []core.Step{
			{Name: "finally_step", Type: "mock"},
		},
	}

	// SkipBuild=true：不执行 build_step
	err := eng.Run(context.Background(), pipeline, nil, nil, nil, RunOpts{SkipBuild: true})
	if err != nil {
		t.Fatalf("SkipBuild run: %v", err)
	}
	order := mock.GetExecutedOrder()
	if len(order) != 2 {
		t.Fatalf("expected 2 steps (deploy+finally, skip build), got %v", order)
	}
	got := make(map[string]bool)
	for _, n := range order {
		got[n] = true
	}
	if !got["deploy_step"] || !got["finally_step"] || got["build_step"] {
		t.Errorf("expected deploy_step and finally_step (no build_step), got %v", order)
	}
}

// TestEngine_FullFlowWithBuild TC-2：完整流转，执行 Build + Deploy + Steps
func TestEngine_FullFlowWithBuild(t *testing.T) {
	mock := NewMockPlugin("mock")
	eng := NewEngine()
	eng.Register(mock)

	pipeline := &core.Pipeline{
		Name: "full",
		Build: []core.Step{
			{Name: "build", Type: "mock"},
		},
		Deploy: []core.Step{
			{Name: "deploy", Type: "mock", Needs: []string{"build"}},
		},
		Steps: []core.Step{
			{Name: "finally", Type: "mock", Needs: []string{"deploy"}},
		},
	}

	err := eng.Run(context.Background(), pipeline, nil, nil, nil)
	if err != nil {
		t.Fatalf("full flow: %v", err)
	}
	order := mock.GetExecutedOrder()
	if len(order) != 3 {
		t.Fatalf("expected 3 steps, got %v", order)
	}
	if order[0] != "build" || order[1] != "deploy" || order[2] != "finally" {
		t.Errorf("expected [build, deploy, finally], got %v", order)
	}
}

// TestEngine_FromVersionMergesParamsSnapshot TC-3/TC-4：晋升时继承历史 params_snapshot
func TestEngine_FromVersionMergesParamsSnapshot(t *testing.T) {
	cap := &varCapturePlugin{}
	eng := NewEngine()
	eng.Register(cap)

	pipeline := &core.Pipeline{
		Name:  "promo",
		Deploy: []core.Step{
			{Name: "deploy", Type: "var_capture", With: map[string]interface{}{"tag": "${artifact_tag}"}},
		},
	}

	err := eng.Run(context.Background(), pipeline, nil, nil, nil, RunOpts{
		SkipBuild:       true,
		FromVersion:     "v1.0.0",
		FromEnv:         "test",
		GetParamsSnapshot: func(env, ver string) (string, error) {
			return `{"artifact_tag":"abc1234"}`, nil
		},
		DeploymentMeta: &DeploymentMeta{Version: "v2.0.0", EnvName: "prod"},
	})
	if err != nil {
		t.Fatalf("promotion run: %v", err)
	}
	if g := cap.capturedWith["tag"]; g != "abc1234" {
		t.Errorf("artifact_tag from history snapshot: got %v, want abc1234", g)
	}
}
