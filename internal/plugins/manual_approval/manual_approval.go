package manual_approval

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
)

const (
	defaultTimeout = 30 * time.Minute
	// ansiBoldYellow 终端高亮（粗体黄色）
	ansiBoldYellow = "\033[1;33m"
	ansiReset      = "\033[0m"
)

// ManualApprovalPlugin 人工确认阀门插件，在关键节点挂起流水线等待人工验收
type ManualApprovalPlugin struct {
	// Stdin 用于单测注入，生产环境为 nil 时使用 os.Stdin
	Stdin io.Reader
}

// NewManualApprovalPlugin 创建 manual_approval 插件实例
func NewManualApprovalPlugin() *ManualApprovalPlugin {
	return &ManualApprovalPlugin{Stdin: nil}
}

// Name 实现 StepPlugin
func (p *ManualApprovalPlugin) Name() string {
	return "manual_approval"
}

// Execute 实现 StepPlugin：打印 message，阻塞等待 stdin 输入 y/n，支持超时与 Context 取消
func (p *ManualApprovalPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	message := maputil.GetString(step.With, "message")
	if message == "" {
		return fmt.Errorf("manual_approval: message is required")
	}
	if ctx.Render != nil {
		message = ctx.Render(message)
	}

	timeout := defaultTimeout
	if timeoutStr := maputil.GetString(step.With, "timeout"); timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil && d > 0 {
			timeout = d
		}
	}

	// 可选：CI 环境通过环境变量自动通过
	if v := os.Getenv("DEPLOYFLOW_APPROVE"); strings.ToLower(v) == "yes" {
		ctx.LogInfo(step.Name, "", "Auto-approved via DEPLOYFLOW_APPROVE=yes")
		fmt.Fprintf(os.Stderr, "%s[manual_approval] auto-approved via DEPLOYFLOW_APPROVE=yes%s\n", ansiBoldYellow, ansiReset)
		return nil
	}

	ctx.LogInfo(step.Name, "", message)

	// 打印高亮提示（stderr 供终端交互，ctx.LogInfo 同步写入 EventBus）
	w := os.Stderr
	fmt.Fprintf(w, "%s%s%s\n", ansiBoldYellow, message, ansiReset)
	ctx.LogInfo(step.Name, "", "Approve? (y/n): ")
	fmt.Fprint(w, "Approve? (y/n): ")

	runCtx := ctx.Context
	deadline, ok := runCtx.Deadline()
	if !ok || time.Until(deadline) > timeout {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// TUI 模式：通过 channel 接收 y/n，避免 Bubble Tea 独占 stdin
	if ctx.ApprovalInputChan != nil && ctx.Bus != nil {
		ctx.Bus.Publish(core.Event{
			Timestamp: time.Now(),
			Type:      core.EventApprovalWaiting,
			StepName:  step.Name,
			Message:   " waiting for y/n",
		})
		select {
		case <-runCtx.Done():
			return fmt.Errorf("manual_approval: %w", runCtx.Err())
		case line := <-ctx.ApprovalInputChan:
			line = strings.TrimSpace(strings.ToLower(line))
			switch line {
			case "y", "yes":
				ctx.LogInfo(step.Name, "", "Approved")
				return nil
			case "n", "no":
				ctx.LogWarn(step.Name, "", "Rejected by user")
				return fmt.Errorf("manual_approval: rejected by user")
			default:
				ctx.LogWarn(step.Name, "", fmt.Sprintf("Invalid input %q (expected y/n)", line))
				return fmt.Errorf("manual_approval: invalid input %q (expected y/n)", line)
			}
		}
	}

	// 非 TUI 模式：从 stdin 读取
	stdin := p.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}

	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(stdin)
		if scanner.Scan() {
			done <- result{line: strings.TrimSpace(strings.ToLower(scanner.Text())), err: nil}
			return
		}
		done <- result{line: "", err: scanner.Err()}
	}()

	select {
	case <-runCtx.Done():
		return fmt.Errorf("manual_approval: %w", runCtx.Err())
	case r := <-done:
		if r.err != nil {
			return fmt.Errorf("manual_approval: read error: %w", r.err)
		}
		switch r.line {
		case "y", "yes":
			ctx.LogInfo(step.Name, "", "Approved")
			return nil
		case "n", "no":
			ctx.LogWarn(step.Name, "", "Rejected by user")
			return fmt.Errorf("manual_approval: rejected by user")
		default:
			ctx.LogWarn(step.Name, "", fmt.Sprintf("Invalid input %q (expected y/n)", r.line))
			return fmt.Errorf("manual_approval: invalid input %q (expected y/n)", r.line)
		}
	}
}

// Rollback 实现 StepPlugin，纯阻塞门控无状态
func (p *ManualApprovalPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	return nil
}

// Uninstall 实现 StepPlugin，无状态插件，卸载时无需操作
func (p *ManualApprovalPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	return nil
}
