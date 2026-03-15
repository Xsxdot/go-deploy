package manual_approval

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
)

func TestManualApproval_ApproveYes(t *testing.T) {
	plugin := &ManualApprovalPlugin{Stdin: strings.NewReader("y\n")}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Proceed?",
			"timeout": "5s",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestManualApproval_ApproveYesFull(t *testing.T) {
	plugin := &ManualApprovalPlugin{Stdin: strings.NewReader("yes\n")}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Continue?",
			"timeout": "5s",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestManualApproval_RejectNo(t *testing.T) {
	plugin := &ManualApprovalPlugin{Stdin: strings.NewReader("n\n")}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Proceed?",
			"timeout": "5s",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error on reject")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("expected rejected error, got %v", err)
	}
}

func TestManualApproval_RejectNoFull(t *testing.T) {
	plugin := &ManualApprovalPlugin{Stdin: strings.NewReader("no\n")}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Proceed?",
			"timeout": "5s",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error on reject")
	}
}

func TestManualApproval_InvalidInput(t *testing.T) {
	plugin := &ManualApprovalPlugin{Stdin: strings.NewReader("maybe\n")}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Proceed?",
			"timeout": "5s",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error on invalid input")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("expected invalid input error, got %v", err)
	}
}

func TestManualApproval_MessageRequired(t *testing.T) {
	plugin := &ManualApprovalPlugin{Stdin: strings.NewReader("y\n")}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{},
	}

	err := plugin.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error when message is missing")
	}
	if !strings.Contains(err.Error(), "message is required") {
		t.Errorf("expected message required error, got %v", err)
	}
}

func TestManualApproval_ContextCancelled(t *testing.T) {
	// Use a reader that never returns (simulates waiting for input)
	plugin := &ManualApprovalPlugin{Stdin: bytes.NewReader(nil)}
	parentCtx, cancel := context.WithCancel(context.Background())
	ctx := core.NewDeployContext(parentCtx, nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Proceed?",
			"timeout": "30m",
		},
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := plugin.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error when context cancelled")
	}
}

func TestManualApproval_Timeout(t *testing.T) {
	// Reader that blocks forever (no input)
	plugin := &ManualApprovalPlugin{Stdin: &blockingReader{}}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Proceed?",
			"timeout": "50ms",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error on timeout")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected timeout error, got %v", err)
	}
}

func TestManualApproval_RenderMessage(t *testing.T) {
	plugin := &ManualApprovalPlugin{Stdin: strings.NewReader("y\n")}
	vars := map[string]string{"version": "v1.2.3"}
	ctx := core.NewDeployContext(context.Background(), nil, nil, vars, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Deploy version ${version}?",
			"timeout": "5s",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestManualApproval_EnvAutoApprove(t *testing.T) {
	orig := os.Getenv("DEPLOYFLOW_APPROVE")
	defer func() { _ = os.Setenv("DEPLOYFLOW_APPROVE", orig) }()

	_ = os.Setenv("DEPLOYFLOW_APPROVE", "yes")

	plugin := &ManualApprovalPlugin{Stdin: &blockingReader{}}
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{
		Type: "manual_approval",
		With: map[string]interface{}{
			"message": "Proceed?",
			"timeout": "1ms",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute with DEPLOYFLOW_APPROVE=yes: %v", err)
	}
}

func TestManualApproval_RollbackNoOp(t *testing.T) {
	plugin := NewManualApprovalPlugin()
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{Type: "manual_approval"}

	err := plugin.Rollback(ctx, step)
	if err != nil {
		t.Fatalf("Rollback should be no-op: %v", err)
	}
}

// blockingReader implements io.Reader but never returns data (blocks on Read)
type blockingReader struct{}

func (r *blockingReader) Read(p []byte) (n int, err error) {
	select {} // block forever
}
