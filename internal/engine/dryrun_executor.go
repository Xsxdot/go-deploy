package engine

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

// DryRunExecutor 实现 core.SSHExecutor，Dry-Run 模式下不执行真实 SSH，仅发布拟执行事件
type DryRunExecutor struct {
	bus core.EventPublisher
}

// NewDryRunExecutor 创建预检模式执行器，bus 用于发布拟执行事件
func NewDryRunExecutor(bus core.EventPublisher) *DryRunExecutor {
	return &DryRunExecutor{bus: bus}
}

// Run 模拟执行，返回成功并发布拟执行命令
func (d *DryRunExecutor) Run(ctx context.Context, target core.Target, cmd string, opts interface{}) (stdout, stderr string, code int, err error) {
	targetID := target.ID()
	if h, ok := sshutil.AsHostTarget(target); ok {
		if targetID == "" {
			targetID = h.Addr + "@" + h.User
		}
	}
	msg := fmt.Sprintf("[DRY-RUN] Would run on %s: %s", targetID, cmd)
	if d.bus != nil {
		d.bus.Publish(core.Event{
			Timestamp: time.Now(),
			Type:      core.EventLog,
			Level:     "INFO",
			TargetID:  targetID,
			Message:   msg,
		})
	}
	return "", "", 0, nil
}

// PutFile 模拟上传，返回成功并发布拟上传路径
func (d *DryRunExecutor) PutFile(ctx context.Context, target core.Target, remotePath string, content []byte) error {
	targetID := target.ID()
	if h, ok := sshutil.AsHostTarget(target); ok {
		if targetID == "" {
			targetID = h.Addr + "@" + h.User
		}
	}
	msg := fmt.Sprintf("[DRY-RUN] Would put file (%d bytes) to %s:%s", len(content), targetID, remotePath)
	if d.bus != nil {
		d.bus.Publish(core.Event{
			Timestamp: time.Now(),
			Type:      core.EventLog,
			Level:     "INFO",
			TargetID:  targetID,
			Message:   msg,
		})
	}
	return nil
}

// PutStream 模拟流式上传，返回成功并发布拟上传路径
func (d *DryRunExecutor) PutStream(ctx context.Context, target core.Target, remotePath string, content io.Reader) error {
	targetID := target.ID()
	if h, ok := sshutil.AsHostTarget(target); ok {
		if targetID == "" {
			targetID = h.Addr + "@" + h.User
		}
	}
	msg := fmt.Sprintf("[DRY-RUN] Would stream to %s:%s", targetID, remotePath)
	if d.bus != nil {
		d.bus.Publish(core.Event{
			Timestamp: time.Now(),
			Type:      core.EventLog,
			Level:     "INFO",
			TargetID:  targetID,
			Message:   msg,
		})
	}
	return nil
}

// Ensure DryRunExecutor implements core.SSHExecutor
var _ core.SSHExecutor = (*DryRunExecutor)(nil)
