package systemd_service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

const (
	defaultSystemdDir = "/etc/systemd/system"
)

var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// rollbackEntry 回滚条目：备份路径与目标 service 路径
type rollbackEntry struct {
	ServicePath string
	BackupPath  string
	HadOriginal bool
	Host        *core.HostTarget
}

func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

func sanitizeForPath(s string) string {
	return sanitizeRe.ReplaceAllString(s, "_")
}

// SystemdServicePlugin systemd 服务配置下发插件，支持模板渲染、远端写入、daemon-reload、enable、restart
type SystemdServicePlugin struct{}

// NewSystemdServicePlugin 创建 systemd_service 插件实例
func NewSystemdServicePlugin() *SystemdServicePlugin {
	return &SystemdServicePlugin{}
}

// Name 实现 StepPlugin
func (p *SystemdServicePlugin) Name() string {
	return "systemd_service"
}

// Execute 实现 StepPlugin：模板渲染 -> 远端写入 /tmp -> sudo mv -> daemon-reload -> enable -> restart
func (p *SystemdServicePlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	name := maputil.GetString(step.With, "name")
	if name == "" {
		return fmt.Errorf("systemd_service: name is required")
	}
	if ctx.Render != nil {
		name = ctx.Render(name)
	}
	if !strings.HasSuffix(name, ".service") {
		name = name + ".service"
	}
	serviceName := name
	baseName := strings.TrimSuffix(name, ".service")

	templatePath := maputil.GetString(step.With, "template")
	inlineTemplate := maputil.GetString(step.With, "inline_template")
	if templatePath == "" && inlineTemplate == "" {
		return fmt.Errorf("systemd_service: template or inline_template is required")
	}

	var tmplContent string
	if templatePath != "" {
		if ctx.Render != nil {
			templatePath = ctx.Render(templatePath)
		}
		templatePath = ctx.ResolvePath(templatePath)
		content, err := os.ReadFile(templatePath)
		if err != nil {
			return fmt.Errorf("systemd_service: read template %q: %w", templatePath, err)
		}
		tmplContent = string(content)
	} else {
		tmplContent = inlineTemplate
		if ctx.Render != nil {
			tmplContent = ctx.Render(tmplContent)
		}
	}

	// 解析 params，对字符串值做 ${var} 替换
	paramsRaw := step.With["params"]
	templateData := make(map[string]interface{})
	if paramsRaw != nil {
		if m, ok := paramsRaw.(map[string]interface{}); ok {
			for k, v := range m {
				templateData[k] = renderValue(ctx, v)
			}
		} else if m, ok := paramsRaw.(map[interface{}]interface{}); ok {
			for k, val := range m {
				if ks, ok := k.(string); ok {
					templateData[ks] = renderValue(ctx, val)
				}
			}
		}
	}

	// 渲染 Go 模板
	tmpl, err := template.New("systemd").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("systemd_service: parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData); err != nil {
		return fmt.Errorf("systemd_service: execute template: %w", err)
	}
	renderedContent := buf.Bytes()

	daemonReload := getBoolWithDefault(step.With, "daemon_reload", true)
	enable := getBoolWithDefault(step.With, "enable", true)
	restart := getBoolWithDefault(step.With, "restart", true)

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
	fn := func(runCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}

		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr + "@" + host.User
		}

		tmpPath := fmt.Sprintf("/tmp/systemd_service_%s_%s.service", sanitizeForPath(step.Name), sanitizeForPath(targetID))
		servicePath := filepath.Join(defaultSystemdDir, serviceName)
		backupPath := fmt.Sprintf("/tmp/systemd_service_backup_%s_%s.service", sanitizeForPath(step.Name), sanitizeForPath(targetID))

		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Deploying systemd unit %s", serviceName))

		// 1. 写入 /tmp（普通用户可写）
		if err := exec.PutFile(runCtx, host, tmpPath, renderedContent); err != nil {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("PutFile failed: %v", err))
			return fmt.Errorf("systemd_service: PutFile failed on %s: %w", targetID, err)
		}

		// 2. 备份已有配置（若存在，需 sudo 读 /etc/systemd/system）
		_, _, testCode, _ := exec.Run(runCtx, host, fmt.Sprintf("sudo test -f %q", servicePath), nil)
		hadOriginal := (testCode == 0)
		if hadOriginal {
			exec.Run(runCtx, host, fmt.Sprintf("sudo cp %q %q", servicePath, backupPath), nil)
		}

		// 3. sudo mv 到 /etc/systemd/system/
		_, stderr, code, err := exec.Run(runCtx, host, fmt.Sprintf("sudo mv %q %q", tmpPath, servicePath), nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("sudo mv failed: %v", err))
			return fmt.Errorf("systemd_service: sudo mv failed on %s: %w (stderr: %s)", targetID, err, stderr)
		}

		// 4. daemon-reload
		if daemonReload {
			_, stderr, code, err = exec.Run(runCtx, host, "sudo systemctl daemon-reload", nil)
			if err != nil || code != 0 {
				ctx.LogError(step.Name, targetID, fmt.Sprintf("daemon-reload failed: %v", err))
				return fmt.Errorf("systemd_service: daemon-reload failed on %s: %w (stderr: %s)", targetID, err, stderr)
			}
		}

		// 5. enable
		if enable {
			_, stderr, code, err = exec.Run(runCtx, host, fmt.Sprintf("sudo systemctl enable %q", baseName), nil)
			if err != nil || code != 0 {
				ctx.LogError(step.Name, targetID, fmt.Sprintf("enable failed: %v", err))
				return fmt.Errorf("systemd_service: enable failed on %s: %w (stderr: %s)", targetID, err, stderr)
			}
		}

		// 6. restart（或 start）
		if restart {
			_, stderr, code, err = exec.Run(runCtx, host, fmt.Sprintf("sudo systemctl restart %q", baseName), nil)
			if err != nil || code != 0 {
				ctx.LogError(step.Name, targetID, fmt.Sprintf("restart failed: %v", err))
				return fmt.Errorf("systemd_service: restart failed on %s: %w (stderr: %s)", targetID, err, stderr)
			}
		}

		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Systemd unit deployed: %s", servicePath))
		mu.Lock()
		rollbackMap[targetID] = &rollbackEntry{
			ServicePath: servicePath,
			BackupPath:  backupPath,
			HadOriginal: hadOriginal,
			Host:        host,
		}
		mu.Unlock()
		return nil
	}

	opts.StepName = step.Name
	err = core.RunParallel(ctx, runTargets, opts, fn)
	ctx.SetRollbackData(step.Name, rollbackMap)
	return err
}

// Uninstall 实现 StepPlugin：停止服务、取消开机自启、删除 service 文件
func (p *SystemdServicePlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	name := maputil.GetString(step.With, "name")
	if name == "" {
		return nil
	}
	if ctx.Render != nil {
		name = ctx.Render(name)
	}
	if !strings.HasSuffix(name, ".service") {
		name = name + ".service"
	}
	baseName := strings.TrimSuffix(name, ".service")
	servicePath := filepath.Join(defaultSystemdDir, name)

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

		// 1. 停止服务并取消开机自启（忽略错误，可能服务已经停了）
		stopCmd := fmt.Sprintf("sudo systemctl stop %q 2>/dev/null || true", baseName)
		disableCmd := fmt.Sprintf("sudo systemctl disable %q 2>/dev/null || true", baseName)
		exec.Run(runCtx, host, stopCmd, nil)
		exec.Run(runCtx, host, disableCmd, nil)

		// 2. 删除 service 文件并 daemon-reload
		rmCmd := fmt.Sprintf("sudo rm -f %q && sudo systemctl daemon-reload", servicePath)
		_, _, code, err := exec.Run(runCtx, host, rmCmd, nil)
		if err != nil || code != 0 {
			ctx.LogWarn(step.Name, targetID, fmt.Sprintf("rm service file failed: %v", err))
			return nil // 卸载时尽量继续，不阻断
		}
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Uninstalled systemd unit %s", name))
		return nil
	}

	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin
func (p *SystemdServicePlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	data, ok := ctx.GetRollbackData(step.Name)
	if !ok || data == nil {
		return nil
	}
	rollbackMap, ok := data.(map[string]*rollbackEntry)
	if !ok || len(rollbackMap) == 0 {
		return nil
	}

	bgCtx := context.Background()
	exec, cleanup := getSSHExecutor(ctx)
	defer cleanup()

	for _, entry := range rollbackMap {
		if entry == nil || entry.Host == nil || entry.ServicePath == "" {
			continue
		}
		baseName := strings.TrimSuffix(filepath.Base(entry.ServicePath), ".service")
		var cmd string
		if entry.HadOriginal && entry.BackupPath != "" {
			// Restore config, reload, restart the old service, then clean backup
			cmd = fmt.Sprintf("sudo cp %q %q && sudo systemctl daemon-reload && sudo systemctl restart %q && sudo rm -f %q", entry.BackupPath, entry.ServicePath, baseName, entry.BackupPath)
		} else {
			// Service was newly created; stop, disable, remove config, daemon-reload
			cmd = fmt.Sprintf("sudo systemctl stop %q 2>/dev/null || true; sudo systemctl disable %q 2>/dev/null || true; sudo rm -f %q && sudo systemctl daemon-reload", baseName, baseName, entry.ServicePath)
		}
		exec.Run(bgCtx, entry.Host, cmd, nil)
	}
	return nil
}

// getBoolWithDefault 当 key 存在时用 GetBool，否则返回 defaultVal
func getBoolWithDefault(m map[string]interface{}, key string, defaultVal bool) bool {
	if m == nil {
		return defaultVal
	}
	if _, ok := m[key]; !ok {
		return defaultVal
	}
	return maputil.GetBool(m, key)
}

func renderValue(ctx *core.DeployContext, v interface{}) interface{} {
	if ctx.Render == nil {
		return v
	}
	switch x := v.(type) {
	case string:
		return ctx.Render(x)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[k] = renderValue(ctx, val)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			if ks, ok := k.(string); ok {
				out[ks] = renderValue(ctx, val)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, val := range x {
			out[i] = renderValue(ctx, val)
		}
		return out
	default:
		return v
	}
}

