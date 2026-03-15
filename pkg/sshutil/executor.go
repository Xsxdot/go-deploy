package sshutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ErrNotHostTarget 表示 Target 不是 HostTarget
var ErrNotHostTarget = errors.New("target is not HostTarget")

// Options Executor 创建时的可选配置
type Options struct {
	KeepAliveInterval time.Duration // 空闲连接保活间隔，0 表示不启用，建议 30s
	Logger            Logger       // 可选，用于 TUI 模式下将错误发布到 EventBus；nil 时使用 NopLogger
	// DialFunc 自定义拨号函数；为 nil 时使用 sshutil.Dial（不支持 Bastion）
	// 需要堡垒机支持时，传入 sshutil.NewDialFunc(infra)
	DialFunc          DialFunc
}

// StreamOptions 流式执行的可选参数
type StreamOptions struct {
	Stdout io.Writer
	Stderr io.Writer
}

// RunOptions 静默执行的可选参数
type RunOptions struct {
	Env map[string]string
}

// Executor 管理 SSH 会话池，提供 Stream 与 Run 两种执行方式
type Executor struct {
	pool     *Pool
	ownsPool bool   // 为 true 时 Close 会关闭 pool；NewFromPool 创建的为 false
	logger   Logger // 错误日志，nil 时使用 nopLogger
}

// New 创建 Executor，内部初始化会话池
func New(opts *Options) *Executor {
	keepAlive := 30 * time.Second
	var logger Logger = nopLogger{}
	dialFn := DialFunc(Dial)
	if opts != nil {
		if opts.KeepAliveInterval > 0 {
			keepAlive = opts.KeepAliveInterval
		}
		if opts.Logger != nil {
			logger = opts.Logger
		}
		if opts.DialFunc != nil {
			dialFn = opts.DialFunc
		}
	}
	pool := NewPool(dialFn, keepAlive, logger)
	return &Executor{pool: pool, ownsPool: true, logger: logger}
}

// NewFromPool 基于已有 Pool 创建 Executor，适用于全局共享 Pool 场景；Close 时不会关闭该 Pool
func NewFromPool(pool *Pool) *Executor {
	return &Executor{pool: pool, ownsPool: false, logger: nopLogger{}}
}

// Stream 流式执行命令，实时输出 stdout/stderr
func (e *Executor) Stream(ctx context.Context, target core.Target, cmd string, opts *StreamOptions) error {
	host, ok := AsHostTarget(target)
	if !ok {
		return ErrNotHostTarget
	}

	client, err := e.pool.GetOrCreate(ctx, host)
	if err != nil {
		e.logger.Error("GetOrCreate 失败", "host", host.Addr, "err", err)
		return err
	}

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	stdout := io.Discard
	stderr := io.Discard
	if opts != nil {
		if opts.Stdout != nil {
			stdout = opts.Stdout
		}
		if opts.Stderr != nil {
			stderr = opts.Stderr
		}
	}

	session.Stdout = stdout
	session.Stderr = stderr

	return session.Run(cmd)
}

// Run 静默执行命令，等待结束后返回 stdout、stderr、退出码
// 实现 core.SSHExecutor 接口，opts 可传 nil 或 *RunOptions
func (e *Executor) Run(ctx context.Context, target core.Target, cmd string, opts interface{}) (stdout, stderr string, code int, err error) {
	var runOpts *RunOptions
	if opts != nil {
		runOpts, _ = opts.(*RunOptions)
	}

	host, ok := AsHostTarget(target)
	if !ok {
		return "", "", -1, ErrNotHostTarget
	}

	client, err := e.pool.GetOrCreate(ctx, host)
	if err != nil {
		return "", "", -1, err
	}

	session, err := client.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer session.Close()

	if runOpts != nil && len(runOpts.Env) > 0 {
		for k, v := range runOpts.Env {
			if err := session.Setenv(k, v); err != nil {
				return "", "", -1, err
			}
		}
	}

	var outBuf, errBuf bytes.Buffer
	session.Stdout = &outBuf
	session.Stderr = &errBuf

	runErr := session.Run(cmd)
	stdout = outBuf.String()
	stderr = errBuf.String()

	if runErr != nil {
		if exitErr, ok := runErr.(*ssh.ExitError); ok {
			return stdout, stderr, exitErr.ExitStatus(), runErr
		}
		return stdout, stderr, -1, runErr
	}
	return stdout, stderr, 0, nil
}

// PutFile 通过 SFTP 上传文件内容到远程路径，避免大文件触发 ARG_MAX
func (e *Executor) PutFile(ctx context.Context, target core.Target, remotePath string, content []byte) error {
	host, ok := AsHostTarget(target)
	if !ok {
		return ErrNotHostTarget
	}

	client, err := e.pool.GetOrCreate(ctx, host)
	if err != nil {
		e.logger.Error("获取 SSH 客户端失败", "host", host.Addr, "err", err)
		return fmt.Errorf("get ssh client: %w", err)
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		e.logger.Error("创建 SFTP 客户端失败", "host", host.Addr, "err", err)
		return fmt.Errorf("new sftp client: %w", err)
	}
	defer sftpClient.Close()

	f, err := sftpClient.Create(remotePath)
	if err != nil {
		e.logger.Error("创建远程文件失败", "host", host.Addr, "path", remotePath, "err", err)
		return fmt.Errorf("create remote file %s: %w", remotePath, err)
	}
	defer f.Close()

	if _, err := f.Write(content); err != nil {
		e.logger.Error("写入远程文件失败", "host", host.Addr, "path", remotePath, "err", err)
		return fmt.Errorf("write to %s: %w", remotePath, err)
	}

	_ = sftpClient.Chmod(remotePath, 0o644)
	return nil
}

// PutStream 通过 SFTP 流式上传内容到远程路径，适用于大文件或压缩流，避免一次性读入内存
func (e *Executor) PutStream(ctx context.Context, target core.Target, remotePath string, content io.Reader) error {
	host, ok := AsHostTarget(target)
	if !ok {
		return ErrNotHostTarget
	}

	client, err := e.pool.GetOrCreate(ctx, host)
	if err != nil {
		e.logger.Error("获取 SSH 客户端失败", "host", host.Addr, "err", err)
		return fmt.Errorf("get ssh client: %w", err)
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		e.logger.Error("创建 SFTP 客户端失败", "host", host.Addr, "err", err)
		return fmt.Errorf("new sftp client: %w", err)
	}
	defer sftpClient.Close()

	f, err := sftpClient.Create(remotePath)
	if err != nil {
		e.logger.Error("创建远程文件失败", "host", host.Addr, "path", remotePath, "err", err)
		return fmt.Errorf("create remote file %s: %w", remotePath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, content); err != nil {
		e.logger.Error("写入远程文件失败", "host", host.Addr, "path", remotePath, "err", err)
		return fmt.Errorf("write to %s: %w", remotePath, err)
	}

	_ = sftpClient.Chmod(remotePath, 0o644)
	return nil
}

// Close 关闭 Executor 及其会话池；NewFromPool 创建的 Executor 不会关闭共享 Pool
func (e *Executor) Close() error {
	if !e.ownsPool {
		return nil
	}
	return e.pool.Close()
}
