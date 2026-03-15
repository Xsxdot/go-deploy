package systemd_check

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Xsxdot/go-deploy/internal/core"
)

// mockSSHExecutor 用于测试的 mock，根据 cmd 返回预定输出
type mockSSHExecutor struct {
	isActiveOut   string
	isActiveCode  int
	isActiveErr   error
	statusOut     string
	journalOut    string
	callCount     int
	isActiveCalls int
}

func (m *mockSSHExecutor) Run(_ context.Context, _ core.Target, cmd string, _ interface{}) (stdout, stderr string, code int, err error) {
	m.callCount++
	if strings.Contains(cmd, "systemctl is-active") {
		m.isActiveCalls++
		return m.isActiveOut, "", m.isActiveCode, m.isActiveErr
	}
	if strings.Contains(cmd, "systemctl status") {
		return m.statusOut, "", 0, nil
	}
	if strings.Contains(cmd, "journalctl") {
		return m.journalOut, "", 0, nil
	}
	return "", "", -1, errors.New("unknown cmd")
}

func (m *mockSSHExecutor) PutFile(_ context.Context, _ core.Target, _ string, _ []byte) error { return nil }
func (m *mockSSHExecutor) PutStream(_ context.Context, _ core.Target, _ string, _ io.Reader) error {
	return nil
}

func TestSystemdCheck_UnitRequired(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewSystemdCheckPlugin()
	step := core.Step{
		Name: "wait_svc",
		Type: "systemd_check",
		With: map[string]interface{}{},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err == nil {
		t.Fatal("expected error when unit is missing")
	}
	if !strings.Contains(err.Error(), "unit is required") {
		t.Errorf("expected 'unit is required', got: %v", err)
	}
}

func TestSystemdCheck_EmptyTargets(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewSystemdCheckPlugin()
	step := core.Step{
		Name: "wait_svc",
		Type: "systemd_check",
		With: map[string]interface{}{"unit": "nginx.service"},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute with no targets should return nil: %v", err)
	}
}

func TestSystemdCheck_SkipsNonHostTargets(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = &mockSSHExecutor{} // 未调用，因为无 HostTarget
	plugin := NewSystemdCheckPlugin()
	step := core.Step{
		Name: "wait_svc",
		Type: "systemd_check",
		With: map[string]interface{}{"unit": "nginx.service"},
	}
	targets := []core.Target{
		&core.K8sTarget{ResourceID: "k1", Context: "ctx", Namespace: "ns"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute with only K8sTarget should return nil: %v", err)
	}
}

func TestSystemdCheck_Success(t *testing.T) {
	mock := &mockSSHExecutor{
		isActiveOut:  "active",
		isActiveCode: 0,
		statusOut:    "● nginx.service",
		journalOut:   "Mar 13 10:00:01 nginx started",
	}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewSystemdCheckPlugin()
	step := core.Step{
		Name: "wait_svc",
		Type: "systemd_check",
		With: map[string]interface{}{
			"unit":          "nginx.service",
			"max_retries":   2,
			"interval":      "1ms",
			"status_log_lines": 0,
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if mock.isActiveCalls != 1 {
		t.Errorf("expected 1 is-active call, got %d", mock.isActiveCalls)
	}
}

func TestSystemdCheck_RetryThenSuccess(t *testing.T) {
	attempt := 0
	mock := &mockSSHExecutor{
		statusOut:  "● nginx.service",
		journalOut: "log line",
	}
	mockFunc := func() (string, int, error) {
		attempt++
		if attempt < 3 {
			return "inactive", 3, errors.New("exit 3")
		}
		return "active", 0, nil
	}
	// 使用闭包更新 mock 的返回值
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = &mockSSHExecutorFunc{
		runFunc: func(_ context.Context, _ core.Target, cmd string, _ interface{}) (string, string, int, error) {
			if strings.Contains(cmd, "systemctl is-active") {
				out, code, err := mockFunc()
				return out, "", code, err
			}
			if strings.Contains(cmd, "systemctl status") {
				return mock.statusOut, "", 0, nil
			}
			if strings.Contains(cmd, "journalctl") {
				return mock.journalOut, "", 0, nil
			}
			return "", "", -1, nil
		},
	}
	plugin := NewSystemdCheckPlugin()
	step := core.Step{
		Name: "wait_svc",
		Type: "systemd_check",
		With: map[string]interface{}{
			"unit":          "nginx.service",
			"max_retries":   5,
			"interval":      "1ms",
			"status_log_lines": 0,
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if attempt != 3 {
		t.Errorf("expected 3 attempts, got %d", attempt)
	}
}

type mockSSHExecutorFunc struct {
	runFunc func(context.Context, core.Target, string, interface{}) (string, string, int, error)
}

func (m *mockSSHExecutorFunc) Run(ctx context.Context, t core.Target, cmd string, opts interface{}) (string, string, int, error) {
	return m.runFunc(ctx, t, cmd, opts)
}
func (m *mockSSHExecutorFunc) PutFile(_ context.Context, _ core.Target, _ string, _ []byte) error { return nil }
func (m *mockSSHExecutorFunc) PutStream(_ context.Context, _ core.Target, _ string, _ io.Reader) error {
	return nil
}

func TestSystemdCheck_FailWithDiagnostic(t *testing.T) {
	mock := &mockSSHExecutor{
		isActiveOut:  "failed",
		isActiveCode: 3,
		statusOut:    "● nginx.service - failed\n  Active: failed",
		journalOut:  "bind() failed (98: Address already in use)",
	}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewSystemdCheckPlugin()
	step := core.Step{
		Name: "wait_svc",
		Type: "systemd_check",
		With: map[string]interface{}{
			"unit":             "nginx.service",
			"max_retries":      1,
			"interval":         "1ms",
			"status_log_lines": 5,
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
	if !strings.Contains(errStr, "not active") {
		t.Errorf("error should contain 'not active', got: %s", errStr)
	}
	if !strings.Contains(errStr, "systemctl status") {
		t.Errorf("error should contain systemctl status output, got: %s", errStr)
	}
	if !strings.Contains(errStr, "journalctl") {
		t.Errorf("error should contain journalctl output, got: %s", errStr)
	}
	if !strings.Contains(errStr, "Address already in use") {
		t.Errorf("error should contain journal log content, got: %s", errStr)
	}
}

func TestSystemdCheck_RollbackNoOp(t *testing.T) {
	plugin := NewSystemdCheckPlugin()
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{Name: "wait_svc", Type: "systemd_check"}

	err := plugin.Rollback(ctx, step)
	if err != nil {
		t.Fatalf("Rollback should be no-op: %v", err)
	}
}
