package http_check

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Xsxdot/go-deploy/internal/core"
)

// mockSSHExecutor 用于测试，根据 curl 命令返回预定的 status code 输出
type mockSSHExecutor struct {
	stdout   string // curl -w 输出，如 "200"
	code     int    // 退出码
	err      error
	callCount int
}

func (m *mockSSHExecutor) Run(_ context.Context, _ core.Target, cmd string, _ interface{}) (stdout, stderr string, code int, err error) {
	m.callCount++
	if strings.Contains(cmd, "curl") {
		return m.stdout, "", m.code, m.err
	}
	return "", "", -1, nil
}

func (m *mockSSHExecutor) PutFile(_ context.Context, _ core.Target, _ string, _ []byte) error { return nil }
func (m *mockSSHExecutor) PutStream(_ context.Context, _ core.Target, _ string, _ io.Reader) error {
	return nil
}

func TestHttpCheck_Success(t *testing.T) {
	mock := &mockSSHExecutor{stdout: "200", code: 0}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewHttpCheckPlugin()
	step := core.Step{
		Name: "health",
		Type: "http_check",
		With: map[string]interface{}{
			"url":            "http://127.0.0.1:8080/health",
			"expected_status": 200,
			"max_retries":    3,
			"interval":       "10ms",
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1", LanAddr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 curl call, got %d", mock.callCount)
	}
}

func TestHttpCheck_ExpectedStatusCustom(t *testing.T) {
	mock := &mockSSHExecutor{stdout: "201", code: 0}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewHttpCheckPlugin()
	step := core.Step{
		Name: "health",
		Type: "http_check",
		With: map[string]interface{}{
			"url":            "http://127.0.0.1:8080/health",
			"expected_status": 201,
			"max_retries":    2,
			"interval":       "10ms",
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestHttpCheck_RetryThenSuccess(t *testing.T) {
	attempt := 0
	mock := &mockSSHExecutorFunc{
		runFunc: func(_ context.Context, _ core.Target, cmd string, _ interface{}) (string, string, int, error) {
			if !strings.Contains(cmd, "curl") {
				return "", "", -1, nil
			}
			attempt++
			if attempt < 3 {
				return "503", "", 0, nil
			}
			return "200", "", 0, nil
		},
	}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewHttpCheckPlugin()
	step := core.Step{
		Name: "health",
		Type: "http_check",
		With: map[string]interface{}{
			"url":         "http://127.0.0.1:8080/health",
			"max_retries": 5,
			"interval":    "10ms",
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

func TestHttpCheck_FailAfterMaxRetries(t *testing.T) {
	mock := &mockSSHExecutor{stdout: "500", code: 0}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewHttpCheckPlugin()
	step := core.Step{
		Name: "health",
		Type: "http_check",
		With: map[string]interface{}{
			"url":         "http://127.0.0.1:8080/health",
			"max_retries": 2,
			"interval":    "10ms",
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if !strings.Contains(err.Error(), "expected status 200, got 500") {
		t.Errorf("expected status mismatch in error, got: %v", err)
	}
}

func TestHttpCheck_EmptyTargets(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewHttpCheckPlugin()
	step := core.Step{
		Name: "health",
		Type: "http_check",
		With: map[string]interface{}{"url": "http://127.0.0.1:9999/health"},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute with no targets should return nil: %v", err)
	}
}

func TestHttpCheck_UrlRequired(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewHttpCheckPlugin()
	step := core.Step{
		Name: "health",
		Type: "http_check",
		With: map[string]interface{}{},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err == nil {
		t.Fatal("expected error when url is missing")
	}
}

func TestHttpCheck_RenderURLWithHostVars(t *testing.T) {
	host := &core.HostTarget{ResourceID: "h1", Addr: "203.0.113.10", LanAddr: "192.168.1.1"}
	urlTemplate := "http://${host.lanAddr}:8080/health"
	rendered := renderURLForHost(urlTemplate, host, nil)
	if rendered != "http://192.168.1.1:8080/health" {
		t.Errorf("renderURLForHost: got %q", rendered)
	}

	urlTemplate2 := "http://${host}:8080/"
	rendered2 := renderURLForHost(urlTemplate2, host, nil)
	if rendered2 != "http://192.168.1.1:8080/" {
		t.Errorf("renderURLForHost ${host}: got %q", rendered2)
	}

	urlTemplate3 := "http://${host.addr}:22/"
	rendered3 := renderURLForHost(urlTemplate3, host, nil)
	if rendered3 != "http://203.0.113.10:22/" {
		t.Errorf("renderURLForHost ${host.addr}: got %q", rendered3)
	}

	// 使用 mock 验证插件执行成功
	mock := &mockSSHExecutor{stdout: "200", code: 0}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewHttpCheckPlugin()
	step := core.Step{
		Name: "health",
		Type: "http_check",
		With: map[string]interface{}{
			"url":         "http://${host}:8080/health",
			"max_retries": 1,
			"interval":    "10ms",
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1", LanAddr: "192.168.1.1"},
	}
	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestHttpCheck_SkipsNonHostTargets(t *testing.T) {
	mock := &mockSSHExecutor{stdout: "200", code: 0}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	ctx.SSHExecutor = mock
	plugin := NewHttpCheckPlugin()
	step := core.Step{
		Name: "health",
		Type: "http_check",
		With: map[string]interface{}{
			"url":         "http://127.0.0.1:8080/health",
			"max_retries": 1,
			"interval":    "10ms",
		},
	}
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"},
		&core.K8sTarget{ResourceID: "k1", Context: "ctx", Namespace: "ns"},
	}

	err := plugin.Execute(ctx, step, targets)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 curl call (only HostTarget), got %d", mock.callCount)
	}
}

func TestHttpCheck_RollbackNoOp(t *testing.T) {
	plugin := NewHttpCheckPlugin()
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{Name: "health", Type: "http_check"}

	err := plugin.Rollback(ctx, step)
	if err != nil {
		t.Fatalf("Rollback should be no-op: %v", err)
	}
}

func TestRenderURLForHost_Order(t *testing.T) {
	host := &core.HostTarget{ResourceID: "h1", Addr: "a.addr", LanAddr: "l.lan"}
	url := "http://${host.lanAddr}:${host.addr}/"
	rendered := renderURLForHost(url, host, nil)
	if rendered != "http://l.lan:a.addr/" {
		t.Errorf("renderURLForHost order: got %q", rendered)
	}
}
