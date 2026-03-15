package backup_state

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

// rollbackEntry 回滚条目，存储备份路径与 Host 引用，避免 Rollback 时依赖 ResolveTargets
type rollbackEntry struct {
	BackupPath string
	Host       *core.HostTarget
}

// getSSHExecutor 优先使用 ctx 的全局 SSHExecutor，否则创建临时实例（调用方需 defer cleanup）
func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// BackupStatePlugin 原地备份插件，部署前将活文件打包备份，支持熔断时回滚
type BackupStatePlugin struct{}

// NewBackupStatePlugin 创建 backup_state 插件实例
func NewBackupStatePlugin() *BackupStatePlugin {
	return &BackupStatePlugin{}
}

// Name 实现 StepPlugin
func (p *BackupStatePlugin) Name() string {
	return "backup_state"
}

// Execute 实现 StepPlugin
func (p *BackupStatePlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	targetPath := maputil.GetString(step.With, "target_path")
	backupDir := maputil.GetString(step.With, "backup_dir")
	if targetPath == "" || backupDir == "" {
		return fmt.Errorf("backup_state: target_path and backup_dir are required")
	}

	if ctx.Render != nil {
		targetPath = ctx.Render(targetPath)
		backupDir = ctx.Render(backupDir)
	}

	version := "unknown"
	if ctx.Render != nil {
		version = ctx.Render("${version}")
	}
	backupPath := filepath.Join(strings.TrimSuffix(backupDir, "/"), "app-"+version+".tar.gz")

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

	var mu sync.Mutex
	rollbackMap := make(map[string]*rollbackEntry)

	opts := core.ParseParallelOptions(step)
	opts.StepName = step.Name
	fn := func(runCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}

		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr + "@" + host.User
		}

		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Backing up %s -> %s", targetPath, backupPath))

		// 1. mkdir -p backup_dir
		_, _, code, err := exec.Run(runCtx, host, fmt.Sprintf("mkdir -p %q", backupDir), nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("mkdir -p failed: %v", err))
			return fmt.Errorf("backup_state: mkdir -p %q failed on %s: %w", backupDir, targetID, err)
		}

		// 2. test -d target_path：若不存在则跳过（首 deploy 无备份），不入 rollbackMap
		_, _, code, _ = exec.Run(runCtx, host, fmt.Sprintf("test -d %q", targetPath), nil)
		if code != 0 {
			return nil // 跳过该 host，无备份可做
		}

		// 3. tar -czf -C targetPath .
		_, _, code, err = exec.Run(runCtx, host, fmt.Sprintf("tar -czf %q -C %q .", backupPath, targetPath), nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("tar -czf failed: %v", err))
			return fmt.Errorf("backup_state: tar -czf failed on %s: %w", targetID, err)
		}

		ctx.LogInfo(step.Name, targetID, "Backup done")

		mu.Lock()
		rollbackMap[targetID] = &rollbackEntry{BackupPath: backupPath, Host: host}
		mu.Unlock()
		return nil
	}

	err := core.RunParallel(ctx, runTargets, opts, fn)
	ctx.SetRollbackData(step.Name, rollbackMap)
	return err
}

// Uninstall 实现 StepPlugin
func (p *BackupStatePlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	return nil
}

// Rollback 实现 StepPlugin
func (p *BackupStatePlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	data, ok := ctx.GetRollbackData(step.Name)
	if !ok || data == nil {
		return nil
	}
	rollbackMap, ok := data.(map[string]*rollbackEntry)
	if !ok || len(rollbackMap) == 0 {
		return nil
	}

	targetPath := maputil.GetString(step.With, "target_path")
	if targetPath != "" && ctx.Render != nil {
		targetPath = ctx.Render(targetPath)
	}
	if targetPath == "" {
		return nil
	}

	bgCtx := context.Background()
	exec, cleanup := getSSHExecutor(ctx)
	defer cleanup()

	// 仅遍历 rollbackMap（Execute 时实际备份过的 host），不依赖 ResolveTargets，避免 CMDB 变化导致目标不一致
	for _, entry := range rollbackMap {
		if entry == nil || entry.Host == nil || entry.BackupPath == "" {
			continue
		}
		host := entry.Host
		backupPath := entry.BackupPath

		// 1. 清空 target_path，移除 transfer 可能残留的脏文件
		exec.Run(bgCtx, host, fmt.Sprintf("rm -rf %q", targetPath), nil)

		// 2. 重建目录
		exec.Run(bgCtx, host, fmt.Sprintf("mkdir -p %q", targetPath), nil)

		// 3. 解压恢复（-C targetPath 与 Execute 的 -C targetPath . 对应）
		_, _, code, _ := exec.Run(bgCtx, host, fmt.Sprintf("tar -xzf %q -C %q", backupPath, targetPath), nil)
		if code != 0 {
			_ = code // 记录但不阻断
		}
	}
	return nil
}
