package docker_container

import (
	"context"
	"strings"
	"testing"

	"github.com/Xsxdot/go-deploy/internal/core"
)

func TestResolveImage(t *testing.T) {
	tests := []struct {
		registry, image, want string
	}{
		{"", "app:v1", "app:v1"},
		{"registry.example.com", "app:v1", "registry.example.com/app:v1"},
		{"registry.example.com/ns", "app:v1", "registry.example.com/ns/app:v1"},
		{"registry.example.com/", "app:v1", "registry.example.com/app:v1"},
		{"reg.com", "ns/app:v1", "ns/app:v1"},
		{"", "registry.com/ns/app:v1", "registry.com/ns/app:v1"},
	}
	for _, tt := range tests {
		got := resolveImage(tt.registry, tt.image)
		if got != tt.want {
			t.Errorf("resolveImage(%q, %q) = %q, want %q", tt.registry, tt.image, got, tt.want)
		}
	}
}

func TestDockerContainer_Required(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewDockerContainerPlugin()

	for _, step := range []core.Step{
		{Name: "run", Type: "docker_container", With: map[string]interface{}{}},
		{Name: "run", Type: "docker_container", With: map[string]interface{}{"container_name": "x"}},
		{Name: "run", Type: "docker_container", With: map[string]interface{}{"image": "x:v1"}},
	} {
		targets := []core.Target{&core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1"}}
		err := plugin.Execute(ctx, step, targets)
		if err == nil {
			t.Errorf("expected error for step with %v", step.With)
		}
		if !strings.Contains(err.Error(), "required") {
			t.Errorf("expected 'required' in error, got: %v", err)
		}
	}
}

func TestDockerContainer_EmptyTargets(t *testing.T) {
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	plugin := NewDockerContainerPlugin()
	step := core.Step{
		Name: "run",
		Type: "docker_container",
		With: map[string]interface{}{
			"container_name": "app",
			"image":         "app:v1",
		},
	}

	err := plugin.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute with no targets should return nil: %v", err)
	}
}

func TestDockerContainer_Rollback(t *testing.T) {
	plugin := NewDockerContainerPlugin()
	ctx := core.NewDeployContext(context.Background(), nil, nil, nil, nil, "")
	step := core.Step{Name: "run", Type: "docker_container"}

	err := plugin.Rollback(ctx, step)
	if err != nil {
		t.Fatalf("Rollback should not error: %v", err)
	}
}
