package symlink_switch

import (
	"context"
	"fmt"
	"strings"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/internal/engine"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

// rollbackSentinelNone 表示回滚时应删除链接（首次部署场景，切换前链接不存在）
const rollbackSentinelNone = "__NONE__"

// getSSHExecutor 优先使用 ctx 的全局 SSHExecutor，否则创建临时实例（调用方需 defer cleanup）
func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// SymlinkSwitchPlugin 软链接原子切换插件
type SymlinkSwitchPlugin struct{}

// NewSymlinkSwitchPlugin 创建 symlink_switch 插件实例
func NewSymlinkSwitchPlugin() *SymlinkSwitchPlugin {
	return &SymlinkSwitchPlugin{}
}

// Name 实现 StepPlugin
func (p *SymlinkSwitchPlugin) Name() string {
	return "symlink_switch"
}

// Execute 实现 StepPlugin
func (p *SymlinkSwitchPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	targetDir := maputil.GetString(step.With, "target_dir")
	linkPath := maputil.GetString(step.With, "link_path")
	if targetDir == "" || linkPath == "" {
		return fmt.Errorf("symlink_switch: target_dir and link_path are required")
	}

	// 渲染变量（如 ${version}）
	if ctx.Render != nil {
		targetDir = ctx.Render(targetDir)
		linkPath = ctx.Render(linkPath)
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
	fn := func(runCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}

		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr + "@" + host.User
		}

		// 1. 若 link_path 已是目录（历史错误部署），先删除，避免 ln -sfn 在其内部创建链接
		_, _, isDirCode, _ := exec.Run(runCtx, host, fmt.Sprintf("test -d %q", linkPath), nil)
		if isDirCode == 0 {
			_, stderr, rmCode, err := exec.Run(runCtx, host, fmt.Sprintf("rm -rf %q", linkPath), nil)
			if err != nil || rmCode != 0 {
				ctx.LogError(step.Name, targetID, fmt.Sprintf("rm -rf %q failed: %v", linkPath, err))
				return fmt.Errorf("symlink_switch: failed to remove existing directory %q on %s: %w (stderr: %s)", linkPath, targetID, err, stderr)
			}
		}

		// 2. 读取当前链接目标，使用 SetStepState 精准记录每台机器的回滚点
		out, _, readlinkCode, _ := exec.Run(runCtx, host, fmt.Sprintf("readlink -f %q", linkPath), nil)
		prevPath := strings.TrimSpace(out)
		if readlinkCode != 0 {
			if readlinkCode == 1 {
				prevPath = rollbackSentinelNone
			} else {
				prevPath = ""
			}
		}

		// 3. 校验 target_dir 存在
		_, stderr, code, err := exec.Run(runCtx, host, fmt.Sprintf("test -d %q", targetDir), nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("target_dir %q does not exist: %v", targetDir, err))
			return fmt.Errorf("symlink_switch: target_dir %q does not exist on %s: %w", targetDir, targetID, err)
		}
		_ = stderr

		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Switching %s -> %s", linkPath, targetDir))

		// 4. 原子切换软链接
		_, _, code, err = exec.Run(runCtx, host, fmt.Sprintf("ln -sfn %q %q", targetDir, linkPath), nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("ln -sfn failed: %v", err))
			return fmt.Errorf("symlink_switch: ln -sfn failed on %s: %w", targetID, err)
		}

		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Switch done: %s -> %s", linkPath, targetDir))

		// 5. 仅在 ln -sfn 成功后用 SetStepState 记录旧路径，供 Rollback 精准还原
		if prevPath != "" {
			ctx.SetStepState(step.Name, targetID, "old_link", prevPath)
		}
		return nil
	}

	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Uninstall 实现 StepPlugin，彻底删除软链接
func (p *SymlinkSwitchPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	linkPath := maputil.GetString(step.With, "link_path")
	if linkPath == "" {
		return nil
	}
	if ctx.Render != nil {
		linkPath = ctx.Render(linkPath)
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
	fn := func(runCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}
		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr + "@" + host.User
		}
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Removing symlink %s", linkPath))
		cmd := fmt.Sprintf("rm -f %q", linkPath)
		_, _, _, _ = exec.Run(runCtx, host, cmd, nil)
		return nil
	}
	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin，使用 GetStepState 精准读取每台机器的旧链接路径并还原
func (p *SymlinkSwitchPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	linkPath := maputil.GetString(step.With, "link_path")
	if linkPath == "" {
		return nil
	}
	if ctx.Render != nil {
		linkPath = ctx.Render(linkPath)
	}

	targets, err := engine.ResolveTargets(step.Roles, ctx.Infra)
	if err != nil {
		return err
	}

	// 使用 context.Background() 确保回滚能完成（deployCtx 可能已取消）
	bgCtx := context.Background()
	exec, cleanup := getSSHExecutor(ctx)
	defer cleanup()

	for _, t := range targets {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			continue
		}
		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr + "@" + host.User
		}
		// 从 StepState 读取该机器的旧链接路径
		prevPath, ok := ctx.GetStepState(step.Name, targetID, "old_link")
		if !ok || prevPath == "" {
			continue
		}
		var cmd string
		if prevPath == rollbackSentinelNone {
			cmd = fmt.Sprintf("rm -f %q", linkPath)
		} else {
			cmd = fmt.Sprintf("ln -sfn %q %q", prevPath, linkPath)
		}
		_, _, code, _ := exec.Run(bgCtx, host, cmd, nil)
		if code != 0 {
			// 记录但不阻断
			_ = code
		}
	}
	return nil
}
