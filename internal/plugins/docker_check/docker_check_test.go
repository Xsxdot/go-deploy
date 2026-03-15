package docker_check

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Xsxdot/go-deploy/internal/core"
)

// mockSSHExecutor 用于测试的 mock，根据 cmd 返回预定输出
type mockSSHExecutor struct {
	inspectRunningOut string
	inspectFullOut    string
	logsOut           string
	inspectCalls      int
}

func (m *mockSSHExecutor) Run(_ context.Context, _ core.Target, cmd string, _ interface{}) (stdout, stderr string, code int, err error) {
	if strings.Contains(cmd, "docker inspect -f") {
		m.inspectCalls++
		return m.inspectRunningOut, "", 0, nil
	}
	if strings.Contains(cmd, "docker inspect ") && !strings.Contains(cmd, "-f ") {
		return m.inspectFullOut, "", 0, nil
	}
	if strings.Contains(cmd, "docker logs") {
		return m.logsOut, "", 0, nil
	}
	return "", "", -1, nil
}

func (m *mockSSHExecutor) PutFile(_ context.Context, _ core.Target, _ string, _ []byte) error { return nil }
func (m *mockSSHExecutor) PutStream(_ context.Context, _ core.Target, _ string, _ io.Reader) error {
	return nil
}

func TestDockerCheck_ContainerRequired(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewDockerCheckPlugin()
	step := core.Step{
		Name: "wait_app",
		Type: "docker_check",
		With: map[string]interface{}{},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err == nil {
		t.Fatal("expected error when container is missing")
	}
	if !strings.Contains(err.Error(), "container is required") {
		t.Errorf("expected 'container is required', got: %v", err)
	}
}

func TestDockerCheck_EmptyTargets(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewDockerCheckPlugin()
	step := core.Step{
		Name: "wait_app",
		Type: "docker_check",
		With: map[string]interface{}{"container": "myapp"},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute with no targets should return nil: %v", err)
	}
}

func TestDockerCheck_SkipsNonHostTargets(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = &mockSSHExecutor{}
	plugin := NewDockerCheckPlugin()
	step := core.Step{
		Name: "wait_app",
		Type: "docker_check",
		With: map[string]interface{}{"container": "myapp"},
	}
	targets := []core.Target{
		&core.K8sTarget{ResourceID: "k1", Context: "ctx", Namespace: "ns"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute with only K8sTarget should return nil: %v", err)
	}
}

func TestDockerCheck_Success(t *testing.T) {
	mock := &mockSSHExecutor{
		inspectRunningOut: "true",
		inspectFullOut:    `{"State":{"Status":"running"}}`,
		logsOut:           "app started",
	}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewDockerCheckPlugin()
	step := core.Step{
		Name: "wait_app",
		Type: "docker_check",
		With: map[string]interface{}{
			"container":   "myapp-1.0",
			"max_retries": 2,
			"interval":    "1ms",
			"log_tail":    0,
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if mock.inspectCalls != 1 {
		t.Errorf("expected 1 inspect call, got %d", mock.inspectCalls)
	}
}

func TestDockerCheck_FailWithDiagnostic(t *testing.T) {
	mock := &mockSSHExecutor{
		inspectRunningOut: "false",
		inspectFullOut:    `"State":{"Status":"exited","Error":"OOMKilled","ExitCode":137}`,
		logsOut:           "panic: out of memory",
	}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewDockerCheckPlugin()
	step := core.Step{
		Name: "wait_app",
		Type: "docker_check",
		With: map[string]interface{}{
			"container":   "myapp-1.0",
			"max_retries": 1,
			"interval":    "1ms",
			"log_tail":    10,
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "not running") {
		t.Errorf("error should contain 'not running', got: %s", errStr)
	}
	if !strings.Contains(errStr, "docker inspect") {
		t.Errorf("error should contain docker inspect output, got: %s", errStr)
	}
	if !strings.Contains(errStr, "docker logs") {
		t.Errorf("error should contain docker logs output, got: %s", errStr)
	}
	if !strings.Contains(errStr, "out of memory") {
		t.Errorf("error should contain log content, got: %s", errStr)
	}
}

func TestDockerCheck_RollbackNoOp(t *testing.T) {
	plugin := NewDockerCheckPlugin()
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{Name: "wait_app", Type: "docker_check"}

	err := plugin.Rollback(ctx, step)
	if err != nil {
		t.Fatalf("Rollback should be no-op: %v", err)
	}
}
