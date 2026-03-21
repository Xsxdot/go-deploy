package tmpl

import (
	"os"
	"testing"
)

func TestRender(t *testing.T) {
	vars := map[string]string{
		"base":   "/opt",
		"user":   "deploy",
		"secret": "xxx",
	}
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{"basic", "path: ${base}/app", "path: /opt/app"},
		{"multiple", "${user}@${base}", "deploy@/opt"},
		{"no match", "plain text", "plain text"},
		{"undefined keeps", "x${unknown}y", "x${unknown}y"},
		{"empty vars", "x${base}y", "x${base}y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Render(tt.template, vars)
			if tt.name == "empty vars" {
				got = Render(tt.template, nil)
				if got != "x${base}y" {
					t.Errorf("Render with nil vars = %q, want x${base}y", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("Render(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}

func TestBuildVars(t *testing.T) {
	os.Setenv("TEST_ENV_VAR", "env_value")
	defer os.Unsetenv("TEST_ENV_VAR")

	globalVars := map[string]string{
		"baseInstallPath": "/opt/services",
		"deployUser":      "deploy",
	}
	variables := map[string]string{
		"agentWorkDir": "${baseInstallPath}/video-agent",
	}
	vars := BuildVars(globalVars, variables)
	if g := vars["baseInstallPath"]; g != "/opt/services" {
		t.Errorf("baseInstallPath = %q", g)
	}
	if g := vars["agentWorkDir"]; g != "/opt/services/video-agent" {
		t.Errorf("agentWorkDir = %q, want /opt/services/video-agent", g)
	}
	if g := vars["TEST_ENV_VAR"]; g != "env_value" {
		t.Errorf("TEST_ENV_VAR = %q", g)
	}
}

func TestBuildVars_Priority(t *testing.T) {
	os.Setenv("OVERRIDE", "from_env")
	defer os.Unsetenv("OVERRIDE")

	globalVars := map[string]string{"OVERRIDE": "from_global"}
	variables := map[string]string{"OVERRIDE": "from_vars"}
	vars := BuildVars(globalVars, variables)
	if vars["OVERRIDE"] != "from_env" {
		t.Errorf("env should override: got %q", vars["OVERRIDE"])
	}
}

func TestRenderValue(t *testing.T) {
	vars := map[string]string{"base": "/opt", "name": "app"}
	input := map[string]interface{}{
		"target": "${base}/${name}/bin",
		"nested": map[string]interface{}{
			"key": "val: ${base}",
		},
		"list": []interface{}{"a", "${base}"},
	}
	got := RenderValue(input, vars).(map[string]interface{})
	if g := got["target"]; g != "/opt/app/bin" {
		t.Errorf("target = %q", g)
	}
	inner := got["nested"].(map[string]interface{})
	if inner["key"] != "val: /opt" {
		t.Errorf("nested key = %q", inner["key"])
	}
	list := got["list"].([]interface{})
	if list[1] != "/opt" {
		t.Errorf("list[1] = %q", list[1])
	}
}

func TestRenderValue_YAMLMap(t *testing.T) {
	vars := map[string]string{"x": "replaced"}
	input := map[interface{}]interface{}{
		"a": "hello ${x}",
		"b": 42,
	}
	got := RenderValue(input, vars)
	out, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("got type %T", got)
	}
	if out["a"] != "hello replaced" {
		t.Errorf("a = %q", out["a"])
	}
	if out["b"] != 42 {
		t.Errorf("b = %v", out["b"])
	}
}

func TestNewRenderer(t *testing.T) {
	vars := map[string]string{"foo": "BAR"}
	render := NewRenderer(vars)
	if render("x${foo}x") != "xBARx" {
		t.Errorf("NewRenderer failed")
	}
}

func TestRender_EnvSecret(t *testing.T) {
	os.Setenv("MY_SECRET_KEY", "s3cr3t")
	defer os.Unsetenv("MY_SECRET_KEY")

	vars := map[string]string{"bucket": "my-bucket"}

	// ${env.MY_SECRET_KEY} 从 os.Getenv 读取，不在 vars 中
	got := Render("key=${env.MY_SECRET_KEY} bucket=${bucket}", vars)
	if got != "key=s3cr3t bucket=my-bucket" {
		t.Errorf("env secret render = %q, want key=s3cr3t bucket=my-bucket", got)
	}

	// 未定义的 env var 保留原样
	got2 := Render("x=${env.UNDEFINED_XYZ_VAR}y", vars)
	if got2 != "x=${env.UNDEFINED_XYZ_VAR}y" {
		t.Errorf("undefined env var should be preserved, got %q", got2)
	}

	// ${env.xxx} 不应出现在 vars 中（安全保证：不落盘）
	if _, ok := vars["MY_SECRET_KEY"]; ok {
		t.Error("secret key must not appear in vars map")
	}
}

// TestRender_DefaultValue 验收 PRD：${var:-default} 当 var 为空或不存在时使用 default
func TestRender_DefaultValue(t *testing.T) {
	tests := []struct {
		name     string
		template string
		vars     map[string]string
		want     string
	}{
		{"var set", "tag: ${image_tag:-${version}}", map[string]string{"image_tag": "v1.0.5", "version": "v1.0.0"}, "tag: v1.0.5"},
		{"var empty use default", "tag: ${image_tag:-${version}}", map[string]string{"image_tag": "", "version": "v1.0.0"}, "tag: v1.0.0"},
		{"var missing use default", "tag: ${image_tag:-${version}}", map[string]string{"version": "v1.0.0"}, "tag: v1.0.0"},
		{"literal default", "port: ${PORT:-8080}", map[string]string{}, "port: 8080"},
		{"dotted var with default", "dir: ${vars.static_dir:-static}", map[string]string{"vars.static_dir": "web"}, "dir: web"},
		{"dotted var missing use default", "dir: ${vars.static_dir:-static}", map[string]string{}, "dir: static"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Render(tt.template, tt.vars)
			if got != tt.want {
				t.Errorf("Render(%q, %v) = %q, want %q", tt.template, tt.vars, got, tt.want)
			}
		})
	}
}

func TestRender_EnvSecretNotInVars(t *testing.T) {
	// 验证 ${env.xxx} 和普通 ${varName} 互不干扰
	os.Setenv("DB_PASSWORD", "pass123")
	defer os.Unsetenv("DB_PASSWORD")

	vars := map[string]string{"host": "db.internal"}
	got := Render("host=${host} pass=${env.DB_PASSWORD}", vars)
	if got != "host=db.internal pass=pass123" {
		t.Errorf("mixed render = %q", got)
	}
}
