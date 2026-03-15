package nginx_config

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
	"github.com/Xsxdot/go-deploy/internal/engine"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

const defaultConfDir = "/etc/nginx/conf.d/"
const defaultFilename = "site.conf"

// rollbackEntry 回滚条目：备份路径与目标配置路径
type rollbackEntry struct {
	ConfPath    string
	BackupPath  string
	HadOriginal bool // 写入前目标文件已存在，回滚时恢复；否则删除新建文件
	Host        *core.HostTarget
}

// getSSHExecutor 优先使用 ctx 的全局 SSHExecutor，否则创建临时实例
func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// NginxConfigPlugin Nginx 配置下发插件，支持 Go 模板渲染与跨角色 upstream 解析
type NginxConfigPlugin struct{}

// NewNginxConfigPlugin 创建 nginx_config 插件实例
func NewNginxConfigPlugin() *NginxConfigPlugin {
	return &NginxConfigPlugin{}
}

// Name 实现 StepPlugin
func (p *NginxConfigPlugin) Name() string {
	return "nginx_config"
}

// Execute 实现 StepPlugin
func (p *NginxConfigPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	templatePath := maputil.GetString(step.With, "template")
	paramsRaw := step.With["params"]
	if templatePath == "" {
		return fmt.Errorf("nginx_config: template is required")
	}
	if ctx.Render != nil {
		templatePath = ctx.Render(templatePath)
	}
	templatePath = ctx.ResolvePath(templatePath)

	// 解析 params，对字符串值做 ${var} 替换
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

	// 跨角色解析 upstream 节点，注入 UpstreamIPs（使用存活节点）
	upstreamRoles := maputil.GetStringSlice(step.With, "upstream_roles")
	if len(upstreamRoles) > 0 && ctx.Infra != nil {
		upstreamTargets, err := engine.ResolveTargets(upstreamRoles, ctx.Infra)
		if err != nil {
			return fmt.Errorf("nginx_config: resolve upstream_roles: %w", err)
		}
		upstreamTargets = ctx.FilterHealthy(upstreamTargets)
		var upstreamIPs []string
		for _, t := range upstreamTargets {
			if host, ok := sshutil.AsHostTarget(t); ok {
				ip := host.GetRouteIP()
				if ip != "" {
					upstreamIPs = append(upstreamIPs, ip)
				}
			}
		}
		templateData["UpstreamIPs"] = upstreamIPs
	} else {
		templateData["UpstreamIPs"] = []string{}
	}

	// 渲染 Go 模板
	tmplContent, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("nginx_config: read template %q: %w", templatePath, err)
	}
	tmpl, err := template.New("nginx").Parse(string(tmplContent))
	if err != nil {
		return fmt.Errorf("nginx_config: parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData); err != nil {
		return fmt.Errorf("nginx_config: execute template: %w", err)
	}
	renderedConf := buf.String()

	// 输出文件名：params.filename 或默认
	paramsMap, _ := paramsRaw.(map[string]interface{})
	if paramsMap == nil {
		if m, _ := paramsRaw.(map[interface{}]interface{}); m != nil {
			paramsMap = make(map[string]interface{})
			for k, v := range m {
				if ks, ok := k.(string); ok {
					paramsMap[ks] = v
				}
			}
		}
	}
	filename := ""
	if paramsMap != nil {
		if f, ok := paramsMap["filename"]; ok {
			if fs, ok := f.(string); ok && fs != "" {
				filename = fs
				if ctx.Render != nil {
					filename = ctx.Render(filename)
				}
			}
		}
	}
	if filename == "" {
		filename = defaultFilename
	}
	if !strings.HasSuffix(filename, ".conf") {
		filename += ".conf"
	}

	// 筛选网关节点
	var runTargets []core.Target
	for _, t := range targets {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			continue
		}
		confDir := host.NginxConfPath
		if confDir == "" && host.HasNginx {
			confDir = defaultConfDir
		}
		if confDir == "" {
			continue
		}
		runTargets = append(runTargets, host)
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

		confDir := host.NginxConfPath
		if confDir == "" {
			confDir = defaultConfDir
		}
		confPath := filepath.Join(confDir, filename)

		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr + "@" + host.User
		}

		// 1. 备份现有配置（若存在）
		backupPath := "/tmp/nginx_config_rollback_" + sanitizeForPath(step.Name) + "_" + sanitizeForPath(targetID) + ".conf"
		exec.Run(runCtx, host, fmt.Sprintf("rm -f %q", backupPath), nil)
		_, _, testCode, _ := exec.Run(runCtx, host, fmt.Sprintf("test -f %q", confPath), nil)
		hadOriginal := (testCode == 0)
		if hadOriginal {
			exec.Run(runCtx, host, fmt.Sprintf("cp %q %q", confPath, backupPath), nil)
		}

		// 2. 写入新配置
		if err := exec.PutFile(runCtx, host, confPath, []byte(renderedConf)); err != nil {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("write config failed: %v", err))
			return fmt.Errorf("nginx_config: write config failed on %s: %w", targetID, err)
		}

		// 3. nginx -t 校验
		_, stderr, code, err := exec.Run(runCtx, host, "nginx -t", nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("nginx -t failed: %v", err))
			return fmt.Errorf("nginx_config: nginx -t failed on %s: %w (stderr: %s)", targetID, err, stderr)
		}

		// 4. nginx -s reload
		_, stderr, code, err = exec.Run(runCtx, host, "nginx -s reload", nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("nginx -s reload failed: %v", err))
			return fmt.Errorf("nginx_config: nginx -s reload failed on %s: %w (stderr: %s)", targetID, err, stderr)
		}

		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Config pushed: %s (nginx reloaded)", confPath))
		mu.Lock()
		rollbackMap[targetID] = &rollbackEntry{ConfPath: confPath, BackupPath: backupPath, HadOriginal: hadOriginal, Host: host}
		mu.Unlock()
		return nil
	}

	opts.StepName = step.Name
	err = core.RunParallel(ctx, runTargets, opts, fn)
	ctx.SetRollbackData(step.Name, rollbackMap)
	return err
}

// Uninstall 实现 StepPlugin：删除 nginx 配置文件并 reload
func (p *NginxConfigPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	paramsRaw := step.With["params"]
	paramsMap, _ := paramsRaw.(map[string]interface{})
	if paramsMap == nil {
		if m, _ := paramsRaw.(map[interface{}]interface{}); m != nil {
			paramsMap = make(map[string]interface{})
			for k, v := range m {
				if ks, ok := k.(string); ok {
					paramsMap[ks] = v
				}
			}
		}
	}
	filename := defaultFilename
	if paramsMap != nil {
		if f, ok := paramsMap["filename"]; ok {
			if fs, ok := f.(string); ok && fs != "" {
				filename = fs
				if ctx.Render != nil {
					filename = ctx.Render(filename)
				}
			}
		}
	}
	if !strings.HasSuffix(filename, ".conf") {
		filename += ".conf"
	}

	var runTargets []core.Target
	for _, t := range targets {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			continue
		}
		confDir := host.NginxConfPath
		if confDir == "" && host.HasNginx {
			confDir = defaultConfDir
		}
		if confDir == "" {
			continue
		}
		runTargets = append(runTargets, host)
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
		confDir := host.NginxConfPath
		if confDir == "" {
			confDir = defaultConfDir
		}
		confPath := filepath.Join(confDir, filename)
		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr + "@" + host.User
		}

		_, _, testCode, _ := exec.Run(runCtx, host, fmt.Sprintf("test -f %q", confPath), nil)
		if testCode != 0 {
			ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Config %s already removed", confPath))
			return nil
		}
		rmCmd := fmt.Sprintf("rm -f %q", confPath)
		_, stderr, code, err := exec.Run(runCtx, host, rmCmd, nil)
		if err != nil || code != 0 {
			ctx.LogWarn(step.Name, targetID, fmt.Sprintf("rm failed: %v (stderr: %s)", err, stderr))
			return nil
		}
		exec.Run(runCtx, host, "nginx -t 2>/dev/null && nginx -s reload 2>/dev/null || true", nil)
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Removed nginx config %s", confPath))
		return nil
	}

	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin
func (p *NginxConfigPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
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
		if entry == nil || entry.Host == nil || entry.ConfPath == "" {
			continue
		}
		var cmd string
		if entry.HadOriginal && entry.BackupPath != "" {
			cmd = fmt.Sprintf("cp %q %q && nginx -t && nginx -s reload && rm -f %q", entry.BackupPath, entry.ConfPath, entry.BackupPath)
		} else {
			// 首次部署：删除新建的配置文件
			cmd = fmt.Sprintf("rm -f %q && nginx -t && nginx -s reload", entry.ConfPath)
		}
		exec.Run(bgCtx, entry.Host, cmd, nil)
	}
	return nil
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

var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeForPath(s string) string {
	return sanitizeRe.ReplaceAllString(s, "_")
}
