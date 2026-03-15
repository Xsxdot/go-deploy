package http_check

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

const (
	defaultExpectedStatus = 200
	defaultMaxRetries     = 10
	defaultInterval       = 5 * time.Second
	defaultHTTPTimeout    = 10 * time.Second
)

// HttpCheckPlugin HTTP 健康拨测插件，确认新版本进程彻底 Ready
type HttpCheckPlugin struct{}

// NewHttpCheckPlugin 创建 http_check 插件实例
func NewHttpCheckPlugin() *HttpCheckPlugin {
	return &HttpCheckPlugin{}
}

// Name 实现 StepPlugin
func (p *HttpCheckPlugin) Name() string {
	return "http_check"
}

// getSSHExecutor 优先使用 ctx 的全局 SSHExecutor，否则创建临时实例
func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// renderURLForHost 针对 HostTarget 渲染 URL，支持 ${host}、${host.lanAddr}、${host.addr}
// 必须按长度从长到短替换，避免 ${host} 误替换 ${host.lanAddr}
func renderURLForHost(template string, host *core.HostTarget, globalRender func(string) string) string {
	replacements := []struct{ k, v string }{
		{"host.lanAddr", host.LanAddr},
		{"host.addr", host.Addr},
		{"host", host.GetRouteIP()},
	}
	s := template
	for _, r := range replacements {
		s = strings.ReplaceAll(s, "${"+r.k+"}", r.v)
	}
	// 再处理全局变量（如 version）
	if globalRender != nil {
		s = globalRender(s)
	}
	return s
}

// shellQuote 将 URL 安全转义为单引号包裹的 shell 参数，避免注入
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// Execute 实现 StepPlugin；通过 SSH 在目标服务器上执行 curl，支持 127.0.0.1 仅本机监听场景
func (p *HttpCheckPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	urlTemplate := maputil.GetString(step.With, "url")
	if urlTemplate == "" {
		return fmt.Errorf("http_check: url is required")
	}

	expectedStatus := maputil.GetInt(step.With, "expected_status")
	if expectedStatus == 0 {
		expectedStatus = defaultExpectedStatus
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

	timeout := defaultHTTPTimeout
	if timeoutStr := maputil.GetString(step.With, "timeout"); timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil && d > 0 {
			timeout = d
		}
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
	opts.StepName = step.Name
	opts.Retries = 0 // http_check 内部已实现重试，禁用 RunParallel 层重试
	fn := func(runCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}

		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr
		}
		url := renderURLForHost(urlTemplate, host, ctx.Render)
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Checking %s (expect %d)", url, expectedStatus))

		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-time.After(interval):
				case <-runCtx.Done():
					return runCtx.Err()
				}
			}

			timeoutSec := int(timeout.Seconds())
			if timeoutSec < 1 {
				timeoutSec = 1
			}
			curlCmd := fmt.Sprintf("curl -sS -o /dev/null -w '%%{http_code}' --connect-timeout %d --max-time %d %s",
				timeoutSec, timeoutSec, shellQuote(url))
			stdout, stderr, code, err := exec.Run(runCtx, host, curlCmd, nil)
			if err != nil || code != 0 {
				if attempt < maxRetries {
					continue
				}
				ctx.LogError(step.Name, targetID, fmt.Sprintf("curl failed: %v (stderr: %s)", err, stderr))
				return fmt.Errorf("http_check on %s (url=%s): %w", host.ID(), url, err)
			}

			statusCode, parseErr := strconv.Atoi(strings.TrimSpace(stdout))
			if parseErr != nil {
				if attempt < maxRetries {
					continue
				}
				ctx.LogError(step.Name, targetID, fmt.Sprintf("invalid curl output: %q", stdout))
				return fmt.Errorf("http_check on %s: failed to parse status from curl output: %q", host.ID(), stdout)
			}

			if statusCode == expectedStatus {
				ctx.LogInfo(step.Name, targetID, fmt.Sprintf("OK: %s returned %d", url, statusCode))
				return nil
			}

			if attempt < maxRetries {
				continue
			}
			ctx.LogError(step.Name, targetID, fmt.Sprintf("Expected status %d, got %d", expectedStatus, statusCode))
			return fmt.Errorf("http_check on %s: expected status %d, got %d (url=%s)", host.ID(), expectedStatus, statusCode, url)
		}

		ctx.LogError(step.Name, targetID, "Max retries exceeded")
		return fmt.Errorf("http_check on %s: max retries exceeded", host.ID())
	}

	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin，纯只读验证插件，无副作用
func (p *HttpCheckPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	return nil
}

// Uninstall 实现 StepPlugin，纯只读验证插件，卸载时无需操作
func (p *HttpCheckPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	return nil
}
