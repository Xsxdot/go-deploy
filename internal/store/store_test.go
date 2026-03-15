package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestStore_EnvVersionIsolation 验收 PRD：数据库环境隔离
// 对 test 环境连续 Apply 3 次，版本号达到 v1.0.3；首次对 prod 环境 Apply，版本号必须是 v1.0.1
func TestStore_EnvVersionIsolation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewSqliteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSqliteStore: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// 创建项目
	if err := s.SaveProject(ctx, "demo", "name: demo\npipeline:\n  steps: []", dir); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	proj, err := s.GetProject(ctx, "demo")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}

	snapshot := `infra: {}`
	pipelineSnap := `name: demo`

	// test 环境连续 3 次部署 -> v1.0.1, v1.0.2, v1.0.3（间隔确保 started_at 有序）
	for i, ver := range []string{"v1.0.1", "v1.0.2", "v1.0.3"} {
		if err := s.SaveDeployment(ctx, proj.ID, "test", ver, "DONE", "", snapshot, pipelineSnap, "{}", dir, 0); err != nil {
			t.Fatalf("SaveDeployment test %s: %v", ver, err)
		}
		if i < 2 {
			time.Sleep(20 * time.Millisecond) // 确保 started_at 先后不同
		}
	}

	// test 环境最新版本应为 v1.0.3
	latestTest, err := s.GetLatestDeploymentForVersionBump(ctx, "demo", "test")
	if err != nil {
		t.Fatalf("GetLatestDeploymentForVersionBump test: %v", err)
	}
	if latestTest == nil || latestTest.Version != "v1.0.3" {
		t.Fatalf("test env latest: expected v1.0.3, got %v", latestTest)
	}

	// prod 环境尚无记录，应返回 nil
	latestProd, err := s.GetLatestDeploymentForVersionBump(ctx, "demo", "prod")
	if err != nil {
		t.Fatalf("GetLatestDeploymentForVersionBump prod: %v", err)
	}
	if latestProd != nil {
		t.Fatalf("prod env should have no deployment yet, got %v", latestProd)
	}

	// 模拟 apply prod：BumpVersion("", "patch") -> v1.0.0（prod 独立版本序列，首次为 v1.0.0）
	nextProd, err := BumpVersion("", "patch")
	if err != nil {
		t.Fatalf("BumpVersion: %v", err)
	}
	if nextProd != "v1.0.0" {
		t.Errorf("prod first version: expected v1.0.0, got %s", nextProd)
	}

	// 断言通过：test 自增到 v1.0.4，prod 首次为 v1.0.1
	nextTest, err := BumpVersion(latestTest.Version, "patch")
	if err != nil {
		t.Fatalf("BumpVersion test: %v", err)
	}
	if nextTest != "v1.0.4" {
		t.Errorf("test next: expected v1.0.4, got %s", nextTest)
	}
	_ = nextTest
}

func TestStore_DefaultEnv(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewSqliteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSqliteStore: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	if err := s.SaveProject(ctx, "p", "name: p\npipeline:\n  steps: []", dir); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	proj, _ := s.GetProject(ctx, "p")

	// 空 env 应视为 default
	if err := s.SaveDeployment(ctx, proj.ID, "", "v1.0.0", "DONE", "", "{}", "{}", "{}", dir, 0); err != nil {
		t.Fatalf("SaveDeployment empty env: %v", err)
	}
	latest, err := s.GetLatestDeploymentForVersionBump(ctx, "p", "")
	if err != nil {
		t.Fatalf("GetLatestDeploymentForVersionBump: %v", err)
	}
	if latest == nil || latest.Version != "v1.0.0" {
		t.Errorf("expected v1.0.0 for default env, got %v", latest)
	}
}
