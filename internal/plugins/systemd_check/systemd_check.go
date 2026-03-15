package systemd_check

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
	defaultExpectedState   = "active"
	defaultMaxRetries      = 10
	defaultInterval        = 5 * time.Second
	defaultStatusLogLines  = 20
)

// SystemdCheckPlugin systemd 服务状态检查插件，通过 SSH 在 HostTarget 上执行 systemctl is-active
type SystemdCheckPlugin struct{}

// NewSystemdCheckPlugin 创建 systemd_check 插件实例
func NewSystemdCheckPlugin() *SystemdCheckPlugin {
	return &SystemdCheckPlugin{}
}

// Name 实现 StepPlugin
func (p *SystemdCheckPlugin) Name() string {
	return "systemd_check"
}

func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// Execute 实现 StepPlugin
func (p *SystemdCheckPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	unit := maputil.GetString(step.With, "unit")
	if unit == "" {
		return fmt.Errorf("systemd_check: unit is required")
	}
	if ctx.Render != nil {
		unit = ctx.Render(unit)
	}

	expectedState := maputil.GetString(step.With, "expected_state")
	if expectedState == "" {
		expectedState = defaultExpectedState
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

	statusLogLines := maputil.GetInt(step.With, "status_log_lines")
	if statusLogLines < 0 {
		statusLogLines = defaultStatusLogLines
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
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Checking systemd unit %s (expect %s)", unit, expectedState))

		gotState := ""
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-time.After(interval):
				case <-runCtx.Done():
					return runCtx.Err()
				}
			}

			cmd := fmt.Sprintf("systemctl is-active %q", unit)
			stdout, _, code, err := exec.Run(runCtx, host, cmd, nil)
			gotState = strings.TrimSpace(stdout)
			if gotState == "" && err != nil {
				gotState = "unknown"
			}
			if err != nil {
				if attempt < maxRetries {
					continue
				}
				ctx.LogError(step.Name, targetID, fmt.Sprintf("systemctl is-active failed: %v", err))
				// 附加诊断后返回
				var diag strings.Builder
				diag.WriteString(fmt.Sprintf("systemd_check on %s: %v\n", host.ID(), err))
				statusCmd := fmt.Sprintf("systemctl status %q --no-pager 2>&1 || true", unit)
				statusOut, _, _, _ := exec.Run(runCtx, host, statusCmd, nil)
				if statusOut != "" {
					diag.WriteString("--- systemctl status ---\n")
					diag.WriteString(strings.TrimSpace(statusOut))
					diag.WriteString("\n")
				}
				if statusLogLines > 0 {
					journalCmd := fmt.Sprintf("journalctl -u %q -n %d --no-pager 2>&1 || true", unit, statusLogLines)
					journalOut, _, _, _ := exec.Run(runCtx, host, journalCmd, nil)
					if journalOut != "" {
						diag.WriteString(fmt.Sprintf("--- journalctl (last %d lines) ---\n", statusLogLines))
						diag.WriteString(strings.TrimSpace(journalOut))
					}
				}
				return fmt.Errorf("%s", diag.String())
			}

			if gotState == expectedState && code == 0 {
				ctx.LogInfo(step.Name, targetID, fmt.Sprintf("OK: unit %s is %s", unit, gotState))
				return nil
			}

			if attempt < maxRetries {
				continue
			}

			ctx.LogError(step.Name, targetID, fmt.Sprintf("unit %s not %s (got: %s)", unit, expectedState, gotState))
			// 失败后附加 systemctl status 与 journalctl 日志
			var diag strings.Builder
			diag.WriteString(fmt.Sprintf("systemd_check on %s: unit %s not %s (got: %s)\n", host.ID(), unit, expectedState, gotState))

			statusCmd := fmt.Sprintf("systemctl status %q --no-pager 2>&1 || true", unit)
			statusOut, _, _, _ := exec.Run(runCtx, host, statusCmd, nil)
			if statusOut != "" {
				diag.WriteString("--- systemctl status ---\n")
				diag.WriteString(strings.TrimSpace(statusOut))
				diag.WriteString("\n")
			}

			if statusLogLines > 0 {
				journalCmd := fmt.Sprintf("journalctl -u %q -n %d --no-pager 2>&1 || true", unit, statusLogLines)
				journalOut, _, _, _ := exec.Run(runCtx, host, journalCmd, nil)
				if journalOut != "" {
					diag.WriteString(fmt.Sprintf("--- journalctl (last %d lines) ---\n", statusLogLines))
					diag.WriteString(strings.TrimSpace(journalOut))
				}
			}

			return fmt.Errorf("%s", diag.String())
		}

		ctx.LogError(step.Name, targetID, "Max retries exceeded")
		return fmt.Errorf("systemd_check on %s: max retries exceeded", host.ID())
	}

	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin，纯只读验证插件，无副作用
func (p *SystemdCheckPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	return nil
}

// Uninstall 实现 StepPlugin，纯只读验证插件，卸载时无需操作
func (p *SystemdCheckPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	return nil
}
