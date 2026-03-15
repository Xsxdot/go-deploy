package remote_command

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

// prefixWriter 在每行输出前添加 [host] 前缀，便于多主机区分
type prefixWriter struct {
	prefix      string
	dst         io.Writer
	atLineStart bool
	mu          sync.Mutex
}

func newPrefixWriter(prefix string, dst io.Writer) io.Writer {
	return &prefixWriter{prefix: prefix, dst: dst, atLineStart: true}
}

func (p *prefixWriter) Write(b []byte) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	total := len(b)
	for len(b) > 0 {
		if p.atLineStart {
			if _, err = p.dst.Write([]byte(p.prefix)); err != nil {
				return total - len(b), err
			}
			p.atLineStart = false
		}
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			written, err := p.dst.Write(b)
			n += written
			return total, err
		}
		written, err := p.dst.Write(b[:i+1])
		n += written
		if err != nil {
			return n, err
		}
		b = b[i+1:]
		p.atLineStart = true
	}
	return n, nil
}

// getStreamExecutor 获取支持 Stream 的 Executor，优先使用 ctx 注入的
func getStreamExecutor(ctx *core.DeployContext) (exec *sshutil.Executor, cleanup func()) {
	if e, ok := ctx.SSHExecutor.(*sshutil.Executor); ok {
		return e, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// RemoteCommandPlugin 远程命令执行插件，通过 SSH 对 HostTarget 执行 Bash 命令
type RemoteCommandPlugin struct{}

// NewRemoteCommandPlugin 创建 remote_command 插件实例
func NewRemoteCommandPlugin() *RemoteCommandPlugin {
	return &RemoteCommandPlugin{}
}

// Name 实现 StepPlugin
func (p *RemoteCommandPlugin) Name() string {
	return "remote_command"
}

// Execute 实现 StepPlugin
func (p *RemoteCommandPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	cmd := maputil.GetString(step.With, "cmd")
	if cmd == "" {
		return fmt.Errorf("remote_command: cmd is required")
	}
	if ctx.Render != nil {
		cmd = ctx.Render(cmd)
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

	var runCtx context.Context = ctx
	if timeoutStr := maputil.GetString(step.With, "timeout"); timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil && d > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
	}

	exec, cleanup := getStreamExecutor(ctx)
	defer cleanup()

	opts := core.ParseParallelOptions(step)
	fn := func(egCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}
		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr
		}
		var stdout, stderr io.Writer
		if ctx.Bus != nil {
			stdout = core.NewLineWriter(func(line string) {
				ctx.LogInfo(step.Name, targetID, line)
			})
			stderr = core.NewLineWriter(func(line string) {
				ctx.LogWarn(step.Name, targetID, line)
			})
		} else {
			prefix := fmt.Sprintf("[%s] ", host.Addr)
			stdout = newPrefixWriter(prefix, os.Stdout)
			stderr = newPrefixWriter(prefix, os.Stderr)
		}
		err := exec.Stream(runCtx, host, cmd, &sshutil.StreamOptions{Stdout: stdout, Stderr: stderr})
		if err != nil {
			return fmt.Errorf("remote_command on %s: %w", host.Addr, err)
		}
		if lw, ok := stdout.(*core.LineWriter); ok {
			lw.Flush()
		}
		if lw, ok := stderr.(*core.LineWriter); ok {
			lw.Flush()
		}
		return nil
	}

	opts.StepName = step.Name
	err := core.RunParallel(ctx, runTargets, opts, fn)
	if err != nil {
		return err
	}

	var hosts []*core.HostTarget
	for _, t := range runTargets {
		if h, ok := sshutil.AsHostTarget(t); ok {
			hosts = append(hosts, h)
		}
	}
	ctx.SetRollbackData(step.Name, hosts)
	return nil
}

// Uninstall 实现 StepPlugin，执行 with 中的 uninstall_cmd（可选）
func (p *RemoteCommandPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	uninstallCmd := maputil.GetString(step.With, "uninstall_cmd")
	if uninstallCmd == "" {
		if ctx.Bus != nil {
			ctx.LogInfo(step.Name, "", "No uninstall_cmd provided, skipping.")
		}
		return nil
	}
	if ctx.Render != nil {
		uninstallCmd = ctx.Render(uninstallCmd)
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

	exec, cleanup := getStreamExecutor(ctx)
	defer cleanup()

	opts := core.ParseParallelOptions(step)
	fn := func(egCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}
		_, stderr, code, err := exec.Run(egCtx, host, uninstallCmd, nil)
		if err != nil || code != 0 {
			return fmt.Errorf("remote_command uninstall on %s: %w (stderr: %s)", host.Addr, err, stderr)
		}
		return nil
	}
	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin
func (p *RemoteCommandPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	rollbackCmd := maputil.GetString(step.With, "rollbackCmd")
	if rollbackCmd == "" {
		return nil
	}
	if ctx.Render != nil {
		rollbackCmd = ctx.Render(rollbackCmd)
	}

	data, ok := ctx.GetRollbackData(step.Name)
	if !ok || data == nil {
		return nil
	}
	hosts, ok := data.([]*core.HostTarget)
	if !ok || len(hosts) == 0 {
		return nil
	}

	bgCtx := context.Background()
	exec, cleanup := getStreamExecutor(ctx)
	defer cleanup()

	for _, host := range hosts {
		if host == nil {
			continue
		}
		_, _, _, _ = exec.Run(bgCtx, host, rollbackCmd, nil)
	}
	return nil
}
