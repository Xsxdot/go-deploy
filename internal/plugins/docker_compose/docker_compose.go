package docker_compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeForPath(s string) string {
	return sanitizeRe.ReplaceAllString(s, "_")
}

// composeRollbackEntry 存储远端 compose 目录与 Host，供 Rollback 使用
type composeRollbackEntry struct {
	RemoteDir        string
	Host             *core.HostTarget
	HadOriginal      bool   // 部署前已有 compose 配置
	BackupComposePath string // 备份的 compose 文件路径
	BackupEnvPath     string // 备份的 .env 文件路径
	ComposeFileName   string // compose 文件名
}

// DockerComposePlugin Docker Compose 编排插件，支持自定义仓库域名
type DockerComposePlugin struct{}

// NewDockerComposePlugin 创建 docker_compose 插件实例
func NewDockerComposePlugin() *DockerComposePlugin {
	return &DockerComposePlugin{}
}

// Name 实现 StepPlugin
func (p *DockerComposePlugin) Name() string {
	return "docker_compose"
}

func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case int, int64:
		return fmt.Sprintf("%d", x)
	case float64:
		return fmt.Sprintf("%g", x)
	case bool:
		if x {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// parseEnvVars 解析 env_vars，支持 map 格式，渲染后生成 .env 文件内容
func parseEnvVars(ctx *core.DeployContext, raw interface{}, registry string) string {
	out := make(map[string]string)
	switch m := raw.(type) {
	case map[string]interface{}:
		for k, val := range m {
			v := toString(val)
			if ctx.Render != nil && v != "" {
				v = ctx.Render(v)
			}
			out[k] = v
		}
	case map[interface{}]interface{}:
		for k, val := range m {
			if ks, ok := k.(string); ok {
				v := toString(val)
				if ctx.Render != nil && v != "" {
					v = ctx.Render(v)
				}
				out[ks] = v
			}
		}
	}

	// 若配置了 registry，注入 DOCKER_REGISTRY
	if registry != "" && out["DOCKER_REGISTRY"] == "" {
		out["DOCKER_REGISTRY"] = registry
	}

	var buf strings.Builder
	for k, v := range out {
		// 简单转义：值含换行或空格时加引号
		if strings.ContainsAny(v, "\n \"") {
			buf.WriteString(fmt.Sprintf("%s=%q\n", k, v))
		} else {
			buf.WriteString(fmt.Sprintf("%s=%s\n", k, v))
		}
	}
	return buf.String()
}

// Execute 实现 StepPlugin
func (p *DockerComposePlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	composeFile := maputil.GetString(step.With, "compose_file")
	projectName := maputil.GetString(step.With, "project_name")
	if composeFile == "" || projectName == "" {
		return fmt.Errorf("docker_compose: compose_file and project_name are required")
	}

	if ctx.Render != nil {
		composeFile = ctx.Render(composeFile)
		projectName = ctx.Render(projectName)
	}

	// 先过滤目标，无 Host 时直接返回（与其他插件一致）
	var runTargets []core.Target
	for _, t := range targets {
		if h, ok := sshutil.AsHostTarget(t); ok {
			runTargets = append(runTargets, h)
		}
	}
	if len(runTargets) == 0 {
		return nil
	}

	composePath := ctx.ResolvePath(composeFile)
	info, err := os.Stat(composePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("docker_compose: compose_file %q does not exist", composePath)
		}
		return fmt.Errorf("docker_compose: stat compose_file: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("docker_compose: compose_file must be a file, not a directory")
	}

	registry := maputil.GetString(step.With, "registry")
	if ctx.Render != nil && registry != "" {
		registry = ctx.Render(registry)
	}

	envVarsRaw := step.With["env_vars"]
	envContent := parseEnvVars(ctx, envVarsRaw, registry)

	// 默认 true（计划约定）
	pullAlways := true
	if v, ok := step.With["pull_always"]; ok {
		if b, ok := v.(bool); ok {
			pullAlways = b
		} else if s, ok := v.(string); ok {
			pullAlways = s == "true" || s == "1"
		}
	}

	runCtx := context.Context(ctx)
	if timeoutStr := maputil.GetString(step.With, "timeout"); timeoutStr != "" {
		if ctx.Render != nil {
			timeoutStr = ctx.Render(timeoutStr)
		}
		if d, err := time.ParseDuration(timeoutStr); err == nil && d > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
	}

	composeData, err := os.ReadFile(composePath)
	if err != nil {
		return fmt.Errorf("docker_compose: read compose_file: %w", err)
	}

	exec, cleanup := getSSHExecutor(ctx)
	defer cleanup()

	composeFileName := filepath.Base(composePath)

	opts := core.ParseParallelOptions(step)
	var rollbackEntries []*composeRollbackEntry
	var rollbackMu sync.Mutex
	fn := func(_ context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}
		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr
		}

		remoteDir := fmt.Sprintf("/opt/deploy/compose/%s", projectName)

		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Uploading compose to %s", remoteDir))

		// 1. mkdir
		mkdirCmd := fmt.Sprintf("mkdir -p %q", remoteDir)
		_, stderr, code, err := exec.Run(runCtx, host, mkdirCmd, nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("mkdir failed: %v (stderr: %s)", err, stderr))
			return fmt.Errorf("docker_compose on %s: mkdir failed: %w", targetID, err)
		}

		// 2. 备份已有 compose 和 .env（若存在），供回滚时恢复
		remoteComposePath := filepath.Join(remoteDir, composeFileName)
		remoteEnvPath := filepath.Join(remoteDir, ".env")
		_, _, testComposeCode, _ := exec.Run(runCtx, host, fmt.Sprintf("test -f %q", remoteComposePath), nil)
		hadOriginal := (testComposeCode == 0)
		var backupComposePath, backupEnvPath string
		if hadOriginal {
			backupComposePath = fmt.Sprintf("/tmp/compose_backup_%s_compose.yml", sanitizeForPath(projectName+"_"+targetID))
			exec.Run(runCtx, host, fmt.Sprintf("sudo cp %q %q", remoteComposePath, backupComposePath), nil)
			_, _, testEnvCode, _ := exec.Run(runCtx, host, fmt.Sprintf("test -f %q", remoteEnvPath), nil)
			if testEnvCode == 0 {
				backupEnvPath = fmt.Sprintf("/tmp/compose_backup_%s_env", sanitizeForPath(projectName+"_"+targetID))
				exec.Run(runCtx, host, fmt.Sprintf("sudo cp %q %q", remoteEnvPath, backupEnvPath), nil)
			}
		}

		// 3. 上传 compose 文件
		if err := exec.PutFile(runCtx, host, remoteComposePath, composeData); err != nil {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("PutFile compose failed: %v", err))
			return fmt.Errorf("docker_compose on %s: put compose failed: %w", targetID, err)
		}

		// 3. 上传 .env
		if envContent != "" {
			envPath := filepath.Join(remoteDir, ".env")
			if err := exec.PutFile(runCtx, host, envPath, []byte(envContent)); err != nil {
				ctx.LogError(step.Name, targetID, fmt.Sprintf("PutFile .env failed: %v", err))
				return fmt.Errorf("docker_compose on %s: put .env failed: %w", targetID, err)
			}
		}

		// 4. docker compose pull（若 pull_always）
		if pullAlways {
			ctx.LogInfo(step.Name, targetID, "Pulling images")
			pullCmd := fmt.Sprintf("cd %q && docker compose -f %q -p %q pull", remoteDir, composeFileName, projectName)
			_, stderr, code, err = exec.Run(runCtx, host, pullCmd, nil)
			if err != nil || code != 0 {
				ctx.LogError(step.Name, targetID, fmt.Sprintf("docker compose pull failed: %v (stderr: %s)", err, stderr))
				return fmt.Errorf("docker_compose on %s: pull failed: %w", targetID, err)
			}
		}

		// 5. docker compose up -d
		ctx.LogInfo(step.Name, targetID, "Starting services")
		upCmd := fmt.Sprintf("cd %q && docker compose -f %q -p %q up -d --remove-orphans", remoteDir, composeFileName, projectName)
		_, stderr, code, err = exec.Run(runCtx, host, upCmd, nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("docker compose up failed: %v (stderr: %s)", err, stderr))
			return fmt.Errorf("docker_compose on %s: up failed: %w", targetID, err)
		}

		rollbackMu.Lock()
		rollbackEntries = append(rollbackEntries, &composeRollbackEntry{
			RemoteDir:         remoteDir,
			Host:              host,
			HadOriginal:       hadOriginal,
			BackupComposePath: backupComposePath,
			BackupEnvPath:     backupEnvPath,
			ComposeFileName:   composeFileName,
		})
		rollbackMu.Unlock()
		return nil
	}

	opts.StepName = step.Name
	err = core.RunParallel(ctx, runTargets, opts, fn)
	if err != nil {
		return err
	}
	ctx.SetRollbackData(step.Name, rollbackEntries)
	return nil
}

// Uninstall 实现 StepPlugin，docker compose down（不含 -v 以保护数据卷）并删除远端 compose 目录
func (p *DockerComposePlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	composeFile := maputil.GetString(step.With, "compose_file")
	projectName := maputil.GetString(step.With, "project_name")
	if projectName == "" {
		return nil
	}
	if ctx.Render != nil {
		composeFile = ctx.Render(composeFile)
		projectName = ctx.Render(projectName)
	}
	composeFileName := filepath.Base(composeFile)

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

	remoteDir := fmt.Sprintf("/opt/deploy/compose/%s", projectName)
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
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Stopping and removing compose project %s", projectName))
		downCmd := fmt.Sprintf("cd %q 2>/dev/null && docker compose -f %q -p %q down 2>/dev/null || true", remoteDir, composeFileName, projectName)
		exec.Run(runCtx, host, downCmd, nil)
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Removing remote dir %s", remoteDir))
		rmCmd := fmt.Sprintf("rm -rf %q", remoteDir)
		exec.Run(runCtx, host, rmCmd, nil)
		return nil
	}
	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin：若有备份则还原旧配置并 up -d，否则仅 down（不含 -v）
func (p *DockerComposePlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	projectName := maputil.GetString(step.With, "project_name")
	if projectName == "" {
		return nil
	}
	if ctx.Render != nil {
		projectName = ctx.Render(projectName)
	}

	composeFile := maputil.GetString(step.With, "compose_file")
	if ctx.Render != nil {
		composeFile = ctx.Render(composeFile)
	}
	composeFileName := filepath.Base(composeFile)

	data, ok := ctx.GetRollbackData(step.Name)
	if !ok || data == nil {
		return nil
	}
	entries, ok := data.([]*composeRollbackEntry)
	if !ok || len(entries) == 0 {
		return nil
	}

	bgCtx := context.Background()
	exec, cleanup := getSSHExecutor(ctx)
	defer cleanup()

	for _, entry := range entries {
		if entry == nil || entry.Host == nil {
			continue
		}
		if entry.HadOriginal && entry.BackupComposePath != "" {
			// 还原旧 compose 并重新 up -d 恢复旧服务
			restoreCmd := fmt.Sprintf("sudo cp %q %q 2>/dev/null || true", entry.BackupComposePath, filepath.Join(entry.RemoteDir, entry.ComposeFileName))
			if entry.BackupEnvPath != "" {
				restoreCmd += fmt.Sprintf(" && sudo cp %q %q 2>/dev/null || true", entry.BackupEnvPath, filepath.Join(entry.RemoteDir, ".env"))
			}
			restoreCmd += fmt.Sprintf(" && cd %q && docker compose -f %q -p %q down 2>/dev/null || true && docker compose -f %q -p %q up -d", entry.RemoteDir, entry.ComposeFileName, projectName, entry.ComposeFileName, projectName)
			exec.Run(bgCtx, entry.Host, restoreCmd, nil)
			// 清理备份文件
			exec.Run(bgCtx, entry.Host, fmt.Sprintf("rm -f %q %q", entry.BackupComposePath, entry.BackupEnvPath), nil)
		} else {
			// 首次部署失败回滚：仅 down，不加 -v 避免删除数据卷
			downCmd := fmt.Sprintf("cd %q && docker compose -f %q -p %q down 2>/dev/null || true", entry.RemoteDir, composeFileName, projectName)
			exec.Run(bgCtx, entry.Host, downCmd, nil)
		}
	}
	return nil
}
