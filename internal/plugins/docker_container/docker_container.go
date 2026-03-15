package docker_container

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

const defaultRestartPolicy = "unless-stopped"

// containerRollbackEntry 回滚条目，存储旧容器镜像与运行参数供 Rollback 恢复
type containerRollbackEntry struct {
	Host          *core.HostTarget
	ContainerName string
	OldImage      string   // 部署前容器使用的镜像，空表示首次部署
	Ports         []string
	Volumes       []string
	RestartPolicy string
}

// DockerContainerPlugin 单容器运行插件，支持自定义仓库域名
type DockerContainerPlugin struct{}

// NewDockerContainerPlugin 创建 docker_container 插件实例
func NewDockerContainerPlugin() *DockerContainerPlugin {
	return &DockerContainerPlugin{}
}

// Name 实现 StepPlugin
func (p *DockerContainerPlugin) Name() string {
	return "docker_container"
}

func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// resolveImage 根据 registry 和 image 组合最终镜像名
// 若 image 已包含 "/"（含 registry），则 registry 不生效
// 否则 fullImage = registry/image，拼接时去除多余斜杠
func resolveImage(registry, image string) string {
	image = strings.TrimSpace(image)
	registry = strings.TrimSpace(registry)
	if image == "" {
		return ""
	}
	if strings.Contains(image, "/") {
		return image
	}
	if registry == "" {
		return image
	}
	registry = strings.TrimSuffix(registry, "/")
	image = strings.TrimPrefix(image, "/")
	return registry + "/" + image
}

// Execute 实现 StepPlugin
func (p *DockerContainerPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	containerName := maputil.GetString(step.With, "container_name")
	image := maputil.GetString(step.With, "image")
	if containerName == "" || image == "" {
		return fmt.Errorf("docker_container: container_name and image are required")
	}

	if ctx.Render != nil {
		containerName = ctx.Render(containerName)
		image = ctx.Render(image)
	}

	registry := maputil.GetString(step.With, "registry")
	if ctx.Render != nil && registry != "" {
		registry = ctx.Render(registry)
	}

	fullImage := resolveImage(registry, image)

	ports := maputil.GetStringSlice(step.With, "ports")
	volumes := maputil.GetStringSlice(step.With, "volumes")
	restartPolicy := maputil.GetString(step.With, "restart_policy")
	if restartPolicy == "" {
		restartPolicy = defaultRestartPolicy
	}
	if ctx.Render != nil && restartPolicy != "" {
		restartPolicy = ctx.Render(restartPolicy)
	}

	pullAlways := maputil.GetBool(step.With, "pull_always")

	// 渲染 ports 和 volumes
	if ctx.Render != nil {
		for i := range ports {
			ports[i] = ctx.Render(ports[i])
		}
		for i := range volumes {
			volumes[i] = ctx.Render(volumes[i])
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

	exec, cleanup := getSSHExecutor(ctx)
	defer cleanup()

	opts := core.ParseParallelOptions(step)
	rollbackMap := make(map[string]*containerRollbackEntry)
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

		// 0. 备份旧容器镜像（若存在），供回滚恢复
		var oldImage string
		stdout, _, inspectCode, _ := exec.Run(runCtx, host, fmt.Sprintf("docker inspect -f '{{.Config.Image}}' %q 2>/dev/null || true", containerName), nil)
		if inspectCode == 0 && strings.TrimSpace(stdout) != "" {
			oldImage = strings.TrimSpace(stdout)
		}

		// 1. docker pull（若 pull_always 或镜像含 registry）
		needPull := pullAlways || strings.Contains(fullImage, "/")
		if needPull {
			ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Pulling image %s", fullImage))
			pullCmd := fmt.Sprintf("docker pull %q", fullImage)
			_, stderr, code, err := exec.Run(runCtx, host, pullCmd, nil)
			if err != nil || code != 0 {
				ctx.LogError(step.Name, targetID, fmt.Sprintf("docker pull failed: %v (stderr: %s)", err, stderr))
				return fmt.Errorf("docker_container on %s: pull failed: %w", targetID, err)
			}
		}

		// 2. docker stop
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Stopping container %s", containerName))
		stopCmd := fmt.Sprintf("docker stop %q 2>/dev/null || true", containerName)
		exec.Run(runCtx, host, stopCmd, nil)

		// 3. docker rm
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Removing container %s", containerName))
		rmCmd := fmt.Sprintf("docker rm %q 2>/dev/null || true", containerName)
		exec.Run(runCtx, host, rmCmd, nil)

		// 4. docker run
		var args []string
		args = append(args, "docker", "run", "-d", "--name", containerName, "--restart", restartPolicy)
		for _, p := range ports {
			if p != "" {
				args = append(args, "-p", p)
			}
		}
		for _, v := range volumes {
			if v != "" {
				args = append(args, "-v", v)
			}
		}
		args = append(args, fullImage)

		runCmd := strings.Join(args, " ")
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Running container: %s", runCmd))
		_, stderr, code, err := exec.Run(runCtx, host, runCmd, nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("docker run failed: %v (stderr: %s)", err, stderr))
			return fmt.Errorf("docker_container on %s: run failed: %w", targetID, err)
		}

		rollbackMu.Lock()
		rollbackMap[targetID] = &containerRollbackEntry{
			Host:          host,
			ContainerName: containerName,
			OldImage:      oldImage,
			Ports:         ports,
			Volumes:       volumes,
			RestartPolicy: restartPolicy,
		}
		rollbackMu.Unlock()
		return nil
	}

	opts.StepName = step.Name
	err := core.RunParallel(ctx, runTargets, opts, fn)
	if err != nil {
		return err
	}
	ctx.SetRollbackData(step.Name, rollbackMap)
	return nil
}

// Uninstall 实现 StepPlugin，彻底移除容器
func (p *DockerContainerPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	containerName := maputil.GetString(step.With, "container_name")
	if containerName == "" {
		return nil
	}
	if ctx.Render != nil {
		containerName = ctx.Render(containerName)
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
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Removing container %s", containerName))
		rmCmd := fmt.Sprintf("docker rm -f %q 2>/dev/null || true", containerName)
		_, _, _, _ = exec.Run(runCtx, host, rmCmd, nil)
		return nil
	}
	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin
func (p *DockerContainerPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	containerName := maputil.GetString(step.With, "container_name")
	if containerName == "" {
		return nil
	}
	if ctx.Render != nil {
		containerName = ctx.Render(containerName)
	}

	data, ok := ctx.GetRollbackData(step.Name)
	if !ok || data == nil {
		return nil
	}
	rollbackMap, ok := data.(map[string]*containerRollbackEntry)
	if !ok || len(rollbackMap) == 0 {
		return nil
	}

	bgCtx := context.Background()
	exec, cleanup := getSSHExecutor(ctx)
	defer cleanup()

	for _, entry := range rollbackMap {
		if entry == nil || entry.Host == nil {
			continue
		}
		// 删除新部署的容器
		rmCmd := fmt.Sprintf("docker rm -f %q 2>/dev/null || true", entry.ContainerName)
		exec.Run(bgCtx, entry.Host, rmCmd, nil)
		// 若有旧镜像，恢复旧容器
		if entry.OldImage != "" {
			var args []string
			args = append(args, "docker", "run", "-d", "--name", entry.ContainerName, "--restart", entry.RestartPolicy)
			for _, p := range entry.Ports {
				if p != "" {
					args = append(args, "-p", p)
				}
			}
			for _, v := range entry.Volumes {
				if v != "" {
					args = append(args, "-v", v)
				}
			}
			args = append(args, entry.OldImage)
			runCmd := strings.Join(args, " ")
			exec.Run(bgCtx, entry.Host, runCmd, nil)
		}
	}
	return nil
}
