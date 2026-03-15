package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/Xsxdot/go-deploy/internal/core"
)

// testdataDir 返回项目 samples/testdata 目录的绝对路径
func testdataDir(t *testing.T) string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..", "samples", "testdata")
}

func TestExpandPipeline_SingleInclude(t *testing.T) {
	baseDir := testdataDir(t)
	pipeline := &core.Pipeline{
		Name: "demo",
		Steps: []core.Step{
			{Name: "Push to OSS", Type: "local_command", With: map[string]interface{}{"cmd": "echo push"}},
			{
				Name:  "Canary Deployment",
				Type:  "include",
				Needs: []string{"Push to OSS"},
				With: map[string]interface{}{
					"template": "templates/canary-release.yaml",
					"vars":     map[string]interface{}{"target_group": "compute_canary"},
				},
			},
			{
				Name:  "Send Notification",
				Type:  "local_command",
				Needs: []string{"Canary Deployment"},
				With:  map[string]interface{}{"cmd": "echo Done!"},
			},
		},
	}

	expanded, err := ExpandPipeline(pipeline, baseDir)
	if err != nil {
		t.Fatalf("ExpandPipeline: %v", err)
	}

	// 应展开为 4 个步骤: Push, Canary.Fetch, Canary.Switch, Send Notification
	if len(expanded.Steps) != 4 {
		t.Errorf("expected 4 steps, got %d", len(expanded.Steps))
	}

	names := make([]string, len(expanded.Steps))
	for i, s := range expanded.Steps {
		names[i] = s.Name
	}
	expectedNames := []string{
		"Push to OSS",
		"Canary Deployment.Fetch & Extract",
		"Canary Deployment.Switch & Restart",
		"Send Notification",
	}
	if !reflect.DeepEqual(names, expectedNames) {
		t.Errorf("step names: got %v, want %v", names, expectedNames)
	}

	// Send Notification 的 needs 应重连到 Canary 的叶子节点
	notifyStep := expanded.Steps[3]
	if !reflect.DeepEqual(notifyStep.Needs, []string{"Canary Deployment.Switch & Restart"}) {
		t.Errorf("Send Notification needs: got %v, want [Canary Deployment.Switch & Restart]", notifyStep.Needs)
	}

	// Fetch & Extract 的 roles 应被 ${vars.target_group} 替换
	fetchStep := expanded.Steps[1]
	if !reflect.DeepEqual(fetchStep.Roles, []string{"compute_canary"}) {
		t.Errorf("Fetch step roles: got %v, want [compute_canary]", fetchStep.Roles)
	}
}

func TestExpandPipeline_LeafRelinking(t *testing.T) {
	baseDir := testdataDir(t)
	pipeline := &core.Pipeline{
		Steps: []core.Step{
			{Name: "A", Type: "local_command", With: map[string]interface{}{}},
			{
				Name:  "Include1",
				Type:  "include",
				Needs: []string{"A"},
				With: map[string]interface{}{
					"template": "templates/canary-release.yaml",
					"vars":     map[string]interface{}{"target_group": "role1"},
				},
			},
			{
				Name:  "Downstream",
				Type:  "local_command",
				Needs: []string{"Include1"},
				With:  map[string]interface{}{},
			},
		},
	}

	expanded, err := ExpandPipeline(pipeline, baseDir)
	if err != nil {
		t.Fatalf("ExpandPipeline: %v", err)
	}

	// Downstream 的 needs 应为 Include1 的叶子节点
	var downstream *core.Step
	for i := range expanded.Steps {
		if expanded.Steps[i].Name == "Downstream" {
			downstream = &expanded.Steps[i]
			break
		}
	}
	if downstream == nil {
		t.Fatal("Downstream step not found")
	}
	if !reflect.DeepEqual(downstream.Needs, []string{"Include1.Switch & Restart"}) {
		t.Errorf("Downstream needs: got %v", downstream.Needs)
	}
}

func TestExpandPipeline_EntryInheritance(t *testing.T) {
	// 创建单步模板，入口节点继承 include 的 needs
	tmpDir := t.TempDir()
	tmplPath := filepath.Join(tmpDir, "single-step.yaml")
	err := os.WriteFile(tmplPath, []byte(`
steps:
  - name: Solo
    type: local_command
    with:
      cmd: "echo solo"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	pipeline := &core.Pipeline{
		Steps: []core.Step{
			{Name: "Pre", Type: "local_command", With: map[string]interface{}{}},
			{
				Name:  "Inc",
				Type:  "include",
				Needs: []string{"Pre"},
				With: map[string]interface{}{
					"template": "single-step.yaml",
					"vars":     map[string]interface{}{},
				},
			},
		},
	}

	expanded, err := ExpandPipeline(pipeline, tmpDir)
	if err != nil {
		t.Fatalf("ExpandPipeline: %v", err)
	}

	var solo *core.Step
	for i := range expanded.Steps {
		if expanded.Steps[i].Name == "Inc.Solo" {
			solo = &expanded.Steps[i]
			break
		}
	}
	if solo == nil {
		t.Fatal("Inc.Solo not found")
	}
	if !reflect.DeepEqual(solo.Needs, []string{"Pre"}) {
		t.Errorf("Inc.Solo needs: got %v, want [Pre]", solo.Needs)
	}
}

func TestExpandPipeline_IncludeDependsOnInclude(t *testing.T) {
	tmpDir := t.TempDir()
	tailPath := filepath.Join(tmpDir, "tail.yaml")
	err := os.WriteFile(tailPath, []byte(`
steps:
  - name: Tail
    type: local_command
    with:
      cmd: "echo tail"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	canaryDir := filepath.Join(tmpDir, "templates")
	if err := os.MkdirAll(canaryDir, 0755); err != nil {
		t.Fatal(err)
	}
	canaryPath := filepath.Join(canaryDir, "canary-release.yaml")
	err = os.WriteFile(canaryPath, []byte(`
steps:
  - name: "Fetch & Extract"
    type: local_command
    with:
      cmd: "echo fetch"
  - name: "Switch & Restart"
    type: local_command
    needs: [ "Fetch & Extract" ]
    with:
      cmd: "echo restart"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	pipeline := &core.Pipeline{
		Steps: []core.Step{
			{Name: "A", Type: "local_command", With: map[string]interface{}{}},
			{
				Name:  "Inc1",
				Type:  "include",
				Needs: []string{"A"},
				With: map[string]interface{}{
					"template": "templates/canary-release.yaml",
					"vars":     map[string]interface{}{"target_group": "x"},
				},
			},
			{
				Name:  "Inc2",
				Type:  "include",
				Needs: []string{"Inc1"},
				With: map[string]interface{}{
					"template": "tail.yaml",
					"vars":     map[string]interface{}{},
				},
			},
		},
	}

	expanded, err := ExpandPipeline(pipeline, tmpDir)
	if err != nil {
		t.Fatalf("ExpandPipeline: %v", err)
	}

	// Inc2.Tail 应依赖 Inc1 的叶子节点
	var tail *core.Step
	for i := range expanded.Steps {
		if expanded.Steps[i].Name == "Inc2.Tail" {
			tail = &expanded.Steps[i]
			break
		}
	}
	if tail == nil {
		t.Fatal("Inc2.Tail not found")
	}
	if !reflect.DeepEqual(tail.Needs, []string{"Inc1.Switch & Restart"}) {
		t.Errorf("Inc2.Tail needs: got %v, want [Inc1.Switch & Restart]", tail.Needs)
	}
}

func TestExpandPipeline_VarsReplacement(t *testing.T) {
	step := core.Step{
		Name:  "test",
		Type:  "local_command",
		Roles: []string{"${vars.target_group}"},
		With:  map[string]interface{}{"role": "${vars.target_group}"},
	}
	vars := map[string]string{"target_group": "compute_canary"}

	rendered := renderTemplateVars(step, vars)

	if !reflect.DeepEqual(rendered.Roles, []string{"compute_canary"}) {
		t.Errorf("Roles: got %v", rendered.Roles)
	}
	if rendered.With["role"] != "compute_canary" {
		t.Errorf("With.role: got %v", rendered.With["role"])
	}
}

func TestExpandPipeline_MissingTemplate(t *testing.T) {
	pipeline := &core.Pipeline{
		Steps: []core.Step{{
			Name: "Bad",
			Type: "include",
			With: map[string]interface{}{"template": "nonexistent.yaml", "vars": map[string]interface{}{}},
		}},
	}

	_, err := ExpandPipeline(pipeline, testdataDir(t))
	if err == nil {
		t.Error("expected error for missing template")
	}
}

func TestLoadPipeline_WithInclude(t *testing.T) {
	path := filepath.Join(testdataDir(t), "include_pipeline.yaml")
	cfg, err := LoadPipeline(path)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}
	if len(cfg.Pipeline.Steps) != 4 {
		t.Errorf("expected 4 expanded steps, got %d", len(cfg.Pipeline.Steps))
	}
	names := make([]string, len(cfg.Pipeline.Steps))
	for i, s := range cfg.Pipeline.Steps {
		names[i] = s.Name
	}
	expected := []string{"Push to OSS", "Canary Deployment.Fetch & Extract", "Canary Deployment.Switch & Restart", "Send Notification"}
	if !reflect.DeepEqual(names, expected) {
		t.Errorf("step names: got %v, want %v", names, expected)
	}
}

func TestExpandPipeline_MissingTemplatePath(t *testing.T) {
	pipeline := &core.Pipeline{
		Steps: []core.Step{{
			Name: "Bad",
			Type: "include",
			With: map[string]interface{}{"vars": map[string]interface{}{}}, // 缺少 template
		}},
	}

	_, err := ExpandPipeline(pipeline, testdataDir(t))
	if err == nil {
		t.Error("expected error for missing template path")
	}
}
