package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPipeline(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "pipeline.yaml")
	err := os.WriteFile(path, []byte(`
name: test-pipeline
env: prod
pipeline:
  steps:
    - name: build
      type: local_command
      with:
        cmd: "go build"
    - name: deploy
      type: transfer
      needs: [build]
      with:
        source: ./bin
        target: /opt/app
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPipeline(path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}
	if cfg.Name != "test-pipeline" {
		t.Errorf("expected name test-pipeline, got %s", cfg.Name)
	}
	if len(cfg.Pipeline.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(cfg.Pipeline.Steps))
	}
	if cfg.Pipeline.Steps[1].Needs[0] != "build" {
		t.Errorf("expected needs [build], got %v", cfg.Pipeline.Steps[1].Needs)
	}
}

func TestLoadInfra(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "infra.yaml")
	err := os.WriteFile(path, []byte(`
hosts:
  - id: node-01
    addr: 192.168.1.101
roles:
  compute: [node-01]
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadInfra(path)
	if err != nil {
		t.Fatalf("LoadInfra: %v", err)
	}
	if len(cfg.Resources) != 1 || cfg.Resources["node-01"].ID() != "node-01" {
		t.Errorf("expected 1 host node-01, got %v", cfg.Resources)
	}
	if len(cfg.Roles["compute"]) != 1 || cfg.Roles["compute"][0] != "node-01" {
		t.Errorf("expected roles compute=[node-01], got %v", cfg.Roles)
	}
}
