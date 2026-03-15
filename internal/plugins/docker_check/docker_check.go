package docker_check

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

const (
	defaultMaxRetries   = 10
	defaultInterval     = 5 * time.Second
	defaultLogTailLines = 50
)

// DockerCheckPlugin 容器运行状态检查插件，通过 SSH 在 HostTarget 上执行 docker inspect
type DockerCheckPlugin struct{}

// NewDockerCheckPlugin 创建 docker_check 插件实例
func NewDockerCheckPlugin() *DockerCheckPlugin {
	return &DockerCheckPlugin{}
}

// Name 实现 StepPlugin
func (p *DockerCheckPlugin) Name() string {
	return "docker_check"
}

func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// Execute 实现 StepPlugin
func (p *DockerCheckPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	container := maputil.GetString(step.With, "container")
	if container == "" {
		return fmt.Errorf("docker_check: container is required")
	}
	if ctx.Render != nil {
		container = ctx.Render(container)
	}

	maxRetries := maputil.GetInt(step.With, "max_retries")
	if maxRetries <= 0 {
		maxRetries = defaultMaxRetries
	}

	interval := defaultInterval
	if intervalStr := maputil.GetString(step.With, "interval"); intervalStr != "" {
		if d, err := time.ParseDuration(intervalStr); err == nil && d > 0 {
			interval = d
		}
	}

	logTail := maputil.GetInt(step.With, "log_tail")
	if logTail < 0 {
		logTail = defaultLogTailLines
	}

	var runTargets []core.Target
	for _, t := range targets {
		if h, ok := sshutil.AsHostTarget(t); ok {
			runTargets = append(runTargets, h)
		}
	}
	if len(runTargets) == 0 {
		return nil
	}

	exec, cleanup := getSSHExecutor(ctx)
	defer cleanup()

	opts := core.ParseParallelOptions(step)
	opts.Retries = 0
	fn := func(runCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}
		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr
		}
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Checking docker container %s", container))

		containerID := container // may need name resolution; docker inspect accepts name
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-time.After(interval):
				case <-runCtx.Done():
					return runCtx.Err()
				}
			}

			cmd := fmt.Sprintf("docker inspect -f '{{.State.Running}}' %q 2>/dev/null || echo 'false'", containerID)
			stdout, stderr, _, err := exec.Run(runCtx, host, cmd, nil)
			if err != nil {
				if attempt < maxRetries {
					continue
				}
				ctx.LogError(step.Name, targetID, fmt.Sprintf("docker inspect failed: %v", err))
				return fmt.Errorf("docker_check on %s: %w", host.ID(), err)
			}

			running := strings.TrimSpace(stdout)
			_ = stderr
			if running == "true" {
				ctx.LogInfo(step.Name, targetID, fmt.Sprintf("OK: container %s is running", containerID))
				return nil
			}

			if attempt < maxRetries {
				continue
			}

			ctx.LogError(step.Name, targetID, fmt.Sprintf("container %s not running after max retries", containerID))
			// 失败后附加 docker inspect 与 docker logs
			var diag strings.Builder
			diag.WriteString(fmt.Sprintf("docker_check on %s: container %s not running after max retries\n", host.ID(), containerID))

			inspectCmd := fmt.Sprintf("docker inspect %q 2>&1 || true", containerID)
			inspectOut, _, _, _ := exec.Run(runCtx, host, inspectCmd, nil)
			if inspectOut != "" {
				// 提取 State 部分以精简输出
				if idx := strings.Index(inspectOut, `"State"`); idx >= 0 {
					end := strings.Index(inspectOut[idx:], "}")
					if end >= 0 {
						stateBlock := inspectOut[idx : idx+end+1]
						diag.WriteString("--- docker inspect (State) ---\n")
						diag.WriteString(strings.TrimSpace(stateBlock))
						diag.WriteString("\n")
					} else {
						diag.WriteString("--- docker inspect ---\n")
						diag.WriteString(strings.TrimSpace(inspectOut))
						diag.WriteString("\n")
					}
				} else {
					diag.WriteString("--- docker inspect ---\n")
					diag.WriteString(strings.TrimSpace(inspectOut))
					diag.WriteString("\n")
				}
			}

			if logTail > 0 {
				logCmd := fmt.Sprintf("docker logs --tail %d %q 2>&1 || true", logTail, containerID)
				logOut, _, _, _ := exec.Run(runCtx, host, logCmd, nil)
				if logOut != "" {
					diag.WriteString(fmt.Sprintf("--- docker logs (last %d lines) ---\n", logTail))
					diag.WriteString(strings.TrimSpace(logOut))
				}
			}

			return fmt.Errorf("%s", diag.String())
		}

		ctx.LogError(step.Name, targetID, "max retries exceeded")
		return fmt.Errorf("docker_check on %s: max retries exceeded", host.ID())
	}

	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin，纯只读验证插件，无副作用
func (p *DockerCheckPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	return nil
}

// Uninstall 实现 StepPlugin，纯只读验证插件，卸载时无需操作
func (p *DockerCheckPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	return nil
}
