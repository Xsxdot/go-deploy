package release_pruner

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

// getSSHExecutor 优先使用 ctx 的全局 SSHExecutor，否则创建临时实例
func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// ReleasePrunerPlugin 版本清理插件，保留最近 N 个版本并删除其余
type ReleasePrunerPlugin struct{}

// NewReleasePrunerPlugin 创建 release_pruner 插件实例
func NewReleasePrunerPlugin() *ReleasePrunerPlugin {
	return &ReleasePrunerPlugin{}
}

// Name 实现 StepPlugin
func (p *ReleasePrunerPlugin) Name() string {
	return "release_pruner"
}

// Execute 实现 StepPlugin
func (p *ReleasePrunerPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	versionsDir := maputil.GetString(step.With, "versions_dir")
	if versionsDir == "" {
		return fmt.Errorf("release_pruner: versions_dir is required")
	}
	keep := maputil.GetInt(step.With, "keep")
	if keep < 1 {
		return fmt.Errorf("release_pruner: keep must be >= 1")
	}
	linkPath := maputil.GetString(step.With, "link_path")
	if linkPath == "" {
		linkPath = core.GetDefaultLinkPath(versionsDir)
	}

	if ctx.Render != nil {
		versionsDir = ctx.Render(versionsDir)
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
			targetID = host.Addr
		}
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Pruning %s (keep %d)", versionsDir, keep))
		// 1. 列出子目录，再按名称排序
		out, _, code, err := exec.Run(runCtx, host, fmt.Sprintf("ls -1 %q 2>/dev/null", versionsDir), nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("ls failed: %v", err))
			return fmt.Errorf("release_pruner: ls failed on %s: %w", host.ID(), err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		var dirs []string
		for _, line := range lines {
			name := strings.TrimSpace(line)
			if name == "" || name == "." || name == ".." {
				continue
			}
			dirs = append(dirs, name)
		}
		sortVersionDirs(dirs)

		// 2. 获取当前软链接指向的目录名
		var currentDir string
		linkOut, _, linkCode, _ := exec.Run(runCtx, host, fmt.Sprintf("readlink -f %q 2>/dev/null", linkPath), nil)
		if linkCode == 0 {
			abs := strings.TrimSpace(linkOut)
			if abs != "" {
				currentDir = filepath.Base(abs)
			}
		}

		// 3. 保留：前 keep 个 + 当前链接指向的目录
		keepSet := make(map[string]bool)
		for i := 0; i < keep && i < len(dirs); i++ {
			keepSet[dirs[i]] = true
		}
		if currentDir != "" {
			keepSet[currentDir] = true
		}

		// 4. 删除其余过期目录
		removed := 0
		for _, d := range dirs {
			if keepSet[d] {
				continue
			}
			if !isSafeDirName(d) {
				continue
			}
			toRemove := filepath.Join(versionsDir, d)
			_, _, rmCode, _ := exec.Run(runCtx, host, fmt.Sprintf("rm -rf %q", toRemove), nil)
			if rmCode != 0 {
				_ = rmCode
			}
			removed++
		}
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Pruned: kept %d, removed %d versions", len(keepSet), removed))
		return nil
	}

	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin，无副作用
func (p *ReleasePrunerPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	return nil
}

// Uninstall 实现 StepPlugin，卸载时无需操作
func (p *ReleasePrunerPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	return nil
}

// ensureVPrefix 为可能为 semver 的字符串补充 v 前缀（semver 要求 v 前缀）
func ensureVPrefix(s string) string {
	if s == "" || strings.HasPrefix(s, "v") {
		return s
	}
	return "v" + s
}

// sortVersionDirs 按语义化版本排序（新版本在前），非 semver 格式回退为字典序
func sortVersionDirs(dirs []string) {
	sort.Slice(dirs, func(i, j int) bool {
		a, b := dirs[i], dirs[j]
		va, vb := ensureVPrefix(a), ensureVPrefix(b)
		if semver.IsValid(va) && semver.IsValid(vb) {
			cmp := semver.Compare(va, vb)
			if cmp != 0 {
				return cmp < 0 // 升序后反转
			}
		}
		return a < b // 无效 semver 或相等时用字典序
	})
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
}

// isSafeDirName 校验目录名，拒绝 ..、绝对路径或包含 / 等危险字符
func isSafeDirName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") {
		return false
	}
	if filepath.IsAbs(name) {
		return false
	}
	return true
}
