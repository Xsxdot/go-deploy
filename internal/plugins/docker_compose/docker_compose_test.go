package docker_compose

import (
	"context"
	"strings"
	"testing"

	"github.com/Xsxdot/go-deploy/internal/core"
)

func TestParseEnvVars(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	out := parseEnvVars(ctx, map[string]interface{}{
		"TAG": "v1",
		"A":   "b",
	}, "")
	if !strings.Contains(out, "TAG=v1") || !strings.Contains(out, "A=b") {
		t.Errorf("parseEnvVars: expected TAG and A, got %q", out)
	}
}

func TestParseEnvVars_RegistryInjection(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	out := parseEnvVars(ctx, map[string]interface{}{"TAG": "v1"}, "registry.example.com")
	if !strings.Contains(out, "DOCKER_REGISTRY=registry.example.com") {
		t.Errorf("parseEnvVars: expected DOCKER_REGISTRY injection, got %q", out)
	}
}

func TestDockerCompose_Required(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewDockerComposePlugin()

	step := core.Step{
		Name: "deploy",
		Type: "docker_compose",
		With: map[string]interface{}{},
	}
	targets := []core.Target{&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"}}
	err := plugin.Execute(ctx, step, targets)
	if err == nil {
		t.Fatal("expected error when compose_file and project_name are missing")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("expected 'required' in error, got: %v", err)
	}
}

func TestDockerCompose_EmptyTargets(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewDockerComposePlugin()
	step := core.Step{
		Name: "deploy",
		Type: "docker_compose",
		With: map[string]interface{}{
			"compose_file": "./nonexistent.yml",
			"project_name": "myapp",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute with no targets should return nil: %v", err)
	}
}

func TestDockerCompose_Rollback(t *testing.T) {
	plugin := NewDockerComposePlugin()
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{Name: "deploy", Type: "docker_compose"}

	err := plugin.Rollback(ctx, step)
	if err != nil {
		t.Fatalf("Rollback should not error: %v", err)
	}
}
