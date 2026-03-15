package local_command

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
)

// LocalCommandPlugin 本地命令执行插件，用于执行 go build、npm run build 等本地构建命令
type LocalCommandPlugin struct{}

// NewLocalCommandPlugin 创建 local_command 插件实例
func NewLocalCommandPlugin() *LocalCommandPlugin {
	return &LocalCommandPlugin{}
}

// Name 实现 StepPlugin
func (p *LocalCommandPlugin) Name() string {
	return "local_command"
}

// Execute 实现 StepPlugin，在本地执行 Shell 命令
func (p *LocalCommandPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	cmd := maputil.GetString(step.With, "cmd")
	if cmd == "" {
		return fmt.Errorf("local_command: cmd is required")
	}
	return p.runCommand(ctx, step, cmd)
}

// Uninstall 实现 StepPlugin，无状态插件卸载时无需操作
func (p *LocalCommandPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	return nil
}

// Rollback 实现 StepPlugin，可选执行 rollbackCmd 进行本地清理
func (p *LocalCommandPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	rollbackCmd := maputil.GetString(step.With, "rollbackCmd")
	if rollbackCmd == "" {
		return nil
	}
	return p.runCommand(ctx, step, rollbackCmd)
}

// runCommand 执行本地命令，支持 workDir 和 env 参数
func (p *LocalCommandPlugin) runCommand(ctx *core.DeployContext, step core.Step, cmdStr string) error {
	if ctx.Render != nil {
		cmdStr = ctx.Render(cmdStr)
	}
	workDir := maputil.GetString(step.With, "workDir")
	envRaw := step.With["env"]

	// 构造 exec.Cmd，使用 sh -c 支持完整 Shell 语义
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)

	// 工作目录：若指定则使用 ResolvePath；否则使用 WorkspaceDir；都空则当前目录
	if workDir != "" {
		if ctx.Render != nil {
			workDir = ctx.Render(workDir)
		}
		cmd.Dir = ctx.ResolvePath(workDir)
	} else if ctx.WorkspaceDir != "" {
		cmd.Dir = ctx.WorkspaceDir
	}

	// 环境变量：合并 os.Environ() 与 step.With["env"]，step env 覆盖
	cmd.Env = mergeEnv(os.Environ(), parseEnvMap(envRaw))

	// 当 EventBus 存在时，输出发往 EventBus 供 TUI/Store 展示；否则直接输出到终端
	if ctx.Bus != nil {
		stdout := core.NewLineWriter(func(line string) { ctx.LogInfo(step.Name, "", line) })
		stderr := core.NewLineWriter(func(line string) { ctx.LogWarn(step.Name, "", line) })
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		defer func() {
			stdout.Flush()
			stderr.Flush()
		}()
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			return fmt.Errorf("local_command: exit code %d", code)
		}
		return fmt.Errorf("local_command: %w", err)
	}
	return nil
}

// parseEnvMap 从 step.With["env"] 解析键值对，支持 map[string]interface{} 和 map[interface{}]interface{}
func parseEnvMap(v interface{}) map[string]string {
	if v == nil {
		return nil
	}
	out := make(map[string]string)
	switch m := v.(type) {
	case map[string]interface{}:
		for k, val := range m {
			out[k] = toString(val)
		}
	case map[interface{}]interface{}:
		for k, val := range m {
			if ks, ok := k.(string); ok {
				out[ks] = toString(val)
			}
		}
	}
	return out
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

// mergeEnv 合并 base 环境变量与 overrides，overrides 中的 key 会覆盖 base
func mergeEnv(base []string, overrides map[string]string) []string {
	envMap := make(map[string]string)
	for _, e := range base {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				envMap[e[:i]] = e[i+1:]
				break
			}
		}
	}
	for k, v := range overrides {
		envMap[k] = v
	}
	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}
	return result
}
