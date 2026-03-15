package transfer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
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

// rollbackEntry 回滚条目，存储目标路径、临时文件路径与 Host 引用
type rollbackEntry struct {
	TargetPath string
	TmpPath    string
	Host       *core.HostTarget
}

// getSSHExecutor 优先使用 ctx 的全局 SSHExecutor，否则创建临时实例
func getSSHExecutor(ctx *core.DeployContext) (exec core.SSHExecutor, cleanup func()) {
	if ctx.SSHExecutor != nil {
		return ctx.SSHExecutor, func() {}
	}
	e := sshutil.New(nil)
	return e, func() { _ = e.Close() }
}

// TransferPlugin 高级传输插件，将本地文件或目录分发到远端 HostTarget
type TransferPlugin struct{}

// NewTransferPlugin 创建 transfer 插件实例
func NewTransferPlugin() *TransferPlugin {
	return &TransferPlugin{}
}

// Name 实现 StepPlugin
func (p *TransferPlugin) Name() string {
	return "transfer"
}

// Execute 实现 StepPlugin
func (p *TransferPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	source := maputil.GetString(step.With, "source")
	target := maputil.GetString(step.With, "target")
	if source == "" || target == "" {
		return fmt.Errorf("transfer: source and target are required")
	}

	if ctx.Render != nil {
		source = ctx.Render(source)
		target = ctx.Render(target)
	}
	source = ctx.ResolvePath(source)

	// 安全提示：target 若非版本独占目录，回滚时 rm -rf 可能误删其他文件
	rawTarget := maputil.GetString(step.With, "target")
	if rawTarget != "" && !strings.Contains(rawTarget, "${") {
		ctx.LogWarn(step.Name, "", "transfer target 建议包含变量（如 ${version}），使用版本独占目录（如 /opt/app/releases/${version}），避免回滚时误删共享目录")
	}

	// 检查 source 存在
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("transfer: source %q does not exist", source)
		}
		return fmt.Errorf("transfer: stat source %q: %w", source, err)
	}

	compress := maputil.GetBool(step.With, "compress")
	chown := maputil.GetString(step.With, "chown")
	if ctx.Render != nil && chown != "" {
		chown = ctx.Render(chown)
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
		sanitizedID := sanitizeForPath(targetID)
		sanitizedStep := sanitizeForPath(step.Name)
		tmpPath := fmt.Sprintf("/tmp/transfer_%s_%s_%d.tar", sanitizedStep, sanitizedID, time.Now().UnixNano())
		if compress && info.IsDir() {
			tmpPath += ".gz"
		}

		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Transferring %s -> %s", source, target))

		// 1. 构建 tar 流并上传
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			if err := writeTarStream(pw, source, info, compress); err != nil {
				pw.CloseWithError(err)
			}
		}()

		if err := exec.PutStream(runCtx, host, tmpPath, pr); err != nil {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("PutStream failed: %v", err))
			return fmt.Errorf("transfer: PutStream failed on %s: %w", targetID, err)
		}

		// 2. mkdir -p target
		_, stderr, code, err := exec.Run(runCtx, host, fmt.Sprintf("mkdir -p %q", target), nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("mkdir -p failed: %v", err))
			return fmt.Errorf("transfer: mkdir -p %q failed on %s: %w", target, targetID, err)
		}
		_ = stderr

		// 3. tar 解压到 target
		extractCmd := fmt.Sprintf("tar -xf %q -C %q", tmpPath, target)
		if compress && info.IsDir() {
			extractCmd = fmt.Sprintf("tar -xzf %q -C %q", tmpPath, target)
		}
		_, stderr, code, err = exec.Run(runCtx, host, extractCmd, nil)
		if err != nil || code != 0 {
			ctx.LogError(step.Name, targetID, fmt.Sprintf("tar extract failed: %v", err))
			return fmt.Errorf("transfer: tar extract failed on %s: %w (stderr: %s)", targetID, err, stderr)
		}

		// 4. chown（若配置）
		if chown != "" {
			_, stderr, code, err = exec.Run(runCtx, host, fmt.Sprintf("chown -R %s %q", chown, target), nil)
			if err != nil || code != 0 {
				ctx.LogError(step.Name, targetID, fmt.Sprintf("chown failed: %v", err))
				return fmt.Errorf("transfer: chown failed on %s: %w (stderr: %s)", targetID, err, stderr)
			}
		}

		ctx.LogInfo(step.Name, targetID, "Transfer done")

		// 5. 删除临时文件
		exec.Run(runCtx, host, fmt.Sprintf("rm -f %q", tmpPath), nil)

		mu.Lock()
		rollbackMap[targetID] = &rollbackEntry{TargetPath: target, TmpPath: tmpPath, Host: host}
		mu.Unlock()
		return nil
	}

	opts.StepName = step.Name
	err = core.RunParallel(ctx, runTargets, opts, fn)
	ctx.SetRollbackData(step.Name, rollbackMap)
	return err
}

// writeTarStream 将 source（文件或目录）打包为 tar 流写入 w，可选 gzip 压缩
func writeTarStream(w io.Writer, source string, info os.FileInfo, compress bool) error {
	if compress && info.IsDir() {
		gw := gzip.NewWriter(w)
		defer gw.Close()
		return writeTar(gw, source, info)
	}
	return writeTar(w, source, info)
}

// writeTar 将 source 打包为 tar 写入 w
func writeTar(w io.Writer, source string, info os.FileInfo) error {
	tw := tar.NewWriter(w)
	defer tw.Close()

	if info.IsDir() {
		return filepath.Walk(source, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil // 跳过根目录本身，只打包其内容
			}
			return addToTar(tw, path, rel, fi)
		})
	}
	// 单文件
	return addToTar(tw, source, filepath.Base(source), info)
}

func addToTar(tw *tar.Writer, path, name string, info os.FileInfo) error {
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

// Uninstall 实现 StepPlugin：删除 step.With 中 target 指定的远端目录；当 ctx.FullPurge 且 full_purge_target 非空时，删除整个应用目录
func (p *TransferPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	target := maputil.GetString(step.With, "target")
	if target == "" {
		return nil
	}
	if ctx.Render != nil {
		target = ctx.Render(target)
	}

	// FullPurge 时优先删除 full_purge_target（整个应用根目录）
	if ctx.FullPurge {
		if fp := maputil.GetString(step.With, "full_purge_target"); fp != "" {
			if ctx.Render != nil {
				fp = ctx.Render(fp)
			}
			target = filepath.Clean(fp)
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
	fn := func(runCtx context.Context, t core.Target) error {
		host, ok := sshutil.AsHostTarget(t)
		if !ok {
			return nil
		}
		targetID := host.ID()
		if targetID == "" {
			targetID = host.Addr + "@" + host.User
		}
		ctx.LogInfo(step.Name, targetID, fmt.Sprintf("Removing %s", target))
		exec.Run(runCtx, host, fmt.Sprintf("rm -rf %q", target), nil)
		return nil
	}

	opts.StepName = step.Name
	return core.RunParallel(ctx, runTargets, opts, fn)
}

// Rollback 实现 StepPlugin，擦除传输的版本目录
func (p *TransferPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
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
		if entry == nil || entry.Host == nil {
			continue
		}
		host := entry.Host
		// rm -rf 擦除目标目录
		if entry.TargetPath != "" {
			exec.Run(bgCtx, host, fmt.Sprintf("rm -rf %q", entry.TargetPath), nil)
		}
		// 清理可能残留的临时文件
		if entry.TmpPath != "" {
			exec.Run(bgCtx, host, fmt.Sprintf("rm -f %q", entry.TmpPath), nil)
		}
	}
	return nil
}
