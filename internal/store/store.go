package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/Xsxdot/go-deploy/internal/bus"
	"github.com/Xsxdot/go-deploy/internal/core"

	_ "modernc.org/sqlite"
)

// Project 注册的项目，从 projects 表读取
type Project struct {
	ID           int64
	Name         string
	PipelineYAML string
	WorkspaceDir string
	CreatedAt    string // 仅 ListProjects 填充
}

// Deployment 部署记录，从 deployments 表读取（含回滚所需快照）
type Deployment struct {
	ID               int64
	ProjectID        int64
	EnvName          string // 环境标识，用于状态隔离
	Version          string
	Status           string
	Message          string
	InfraSnapshot    string
	PipelineSnapshot string
	ParamsSnapshot   string // JSON，合并后的最终变量字典，晋升/回滚时用于变量继承
	WorkspaceDir     string
	DurationMs       int64
	Outputs          string // JSON，如 {"artifact_url": "https://oss.../app.tar.gz"}
	StartedAt        string // 仅 ListDeployments 填充，格式为 SQLite DATETIME
}

// DefaultDBPath 返回默认数据库路径 ~/.deploy/deploy.db
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("获取用户主目录失败", "err", err)
		return "", fmt.Errorf("user home dir: %w", err)
	}
	dir := filepath.Join(home, ".deploy")
	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Error("创建 .deploy 目录失败", "dir", dir, "err", err)
		return "", fmt.Errorf("mkdir .deploy: %w", err)
	}
	return filepath.Join(dir, "deploy.db"), nil
}

// SqliteStore SQLite 存储实现，同时实现 bus.EventHandler 用于审计日志闭环
type SqliteStore struct {
	db     *sql.DB
	closed atomic.Bool
}

// NewSqliteStore 创建并初始化 SqliteStore
func NewSqliteStore(dbPath string) (*SqliteStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Error("创建数据库目录失败", "dir", dir, "err", err)
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		slog.Error("打开 SQLite 失败", "path", dbPath, "err", err)
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		slog.Error("设置 WAL 模式失败", "err", err)
		return nil, fmt.Errorf("PRAGMA journal_mode=WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		slog.Error("设置外键失败", "err", err)
		return nil, fmt.Errorf("PRAGMA foreign_keys: %w", err)
	}
	if _, err := db.Exec(Schema); err != nil {
		_ = db.Close()
		slog.Error("执行 schema 失败", "err", err)
		return nil, fmt.Errorf("exec schema: %w", err)
	}
	if err := migrateDeploymentsOutputs(db); err != nil {
		_ = db.Close()
		slog.Error("迁移 deployments 表失败", "err", err)
		return nil, fmt.Errorf("migrate deployments: %w", err)
	}
	if err := migrateDeploymentsEnv(db); err != nil {
		_ = db.Close()
		slog.Error("迁移 deployments env_name 失败", "err", err)
		return nil, fmt.Errorf("migrate deployments env: %w", err)
	}
	if err := migrateDeploymentsParamsSnapshot(db); err != nil {
		_ = db.Close()
		slog.Error("迁移 deployments params_snapshot 失败", "err", err)
		return nil, fmt.Errorf("migrate deployments params_snapshot: %w", err)
	}
	return &SqliteStore{db: db}, nil
}

// migrateDeploymentsOutputs 为已有 deployments 表补充 outputs 列（兼容旧数据库）
func migrateDeploymentsOutputs(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(deployments)")
	if err != nil {
		return err
	}
	defer rows.Close()
	hasOutputs := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "outputs" {
			hasOutputs = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasOutputs {
		return nil
	}
	_, err = db.Exec("ALTER TABLE deployments ADD COLUMN outputs TEXT")
	return err
}

// migrateDeploymentsEnv 为已有 deployments 表补充 env_name 列，并更新唯一索引（兼容旧数据库）
func migrateDeploymentsEnv(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(deployments)")
	if err != nil {
		return err
	}
	defer rows.Close()
	hasEnvName := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "env_name" {
			hasEnvName = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasEnvName {
		if _, err := db.Exec("ALTER TABLE deployments ADD COLUMN env_name TEXT NOT NULL DEFAULT 'default'"); err != nil {
			return err
		}
	}
	_, _ = db.Exec("DROP INDEX IF EXISTS idx_deployments_project_version")
	_, _ = db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_deployments_project_env_version ON deployments(project_id, env_name, version)")
	return nil
}

// migrateDeploymentsParamsSnapshot 为已有 deployments 表补充 params_snapshot 列（兼容旧数据库）
func migrateDeploymentsParamsSnapshot(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(deployments)")
	if err != nil {
		return err
	}
	defer rows.Close()
	hasParamsSnapshot := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "params_snapshot" {
			hasParamsSnapshot = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasParamsSnapshot {
		_, err = db.Exec("ALTER TABLE deployments ADD COLUMN params_snapshot TEXT NOT NULL DEFAULT '{}'")
		return err
	}
	return nil
}

// HasGlobalInfra 检查全局 infra 是否已存在
func (s *SqliteStore) HasGlobalInfra(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM global_infra WHERE id = 1").Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ProjectExists 检查指定名称的 project 是否已存在
func (s *SqliteStore) ProjectExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE name = ?", name).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SaveGlobalInfra 保存或更新全局 infra（单例 upsert）
func (s *SqliteStore) SaveGlobalInfra(ctx context.Context, infraYAML string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO global_infra (id, infra_yaml) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET infra_yaml = excluded.infra_yaml, updated_at = CURRENT_TIMESTAMP
	`, infraYAML)
	return err
}

// GetGlobalInfra 获取全局 infra
func (s *SqliteStore) GetGlobalInfra(ctx context.Context) (string, error) {
	var infraYAML string
	err := s.db.QueryRowContext(ctx, "SELECT infra_yaml FROM global_infra WHERE id = 1").Scan(&infraYAML)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("global infra not found (run 'deploy infra load -f infra.yaml' first)")
	}
	if err != nil {
		return "", err
	}
	return infraYAML, nil
}

// SaveProject 创建或更新 project（upsert by name）
func (s *SqliteStore) SaveProject(ctx context.Context, name, pipelineYAML, workspaceDir string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (name, pipeline_yaml, workspace_dir) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET pipeline_yaml = excluded.pipeline_yaml, workspace_dir = excluded.workspace_dir, updated_at = CURRENT_TIMESTAMP
	`, name, pipelineYAML, workspaceDir)
	return err
}

// GetProject 获取指定名称的 project
func (s *SqliteStore) GetProject(ctx context.Context, name string) (*Project, error) {
	var p Project
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, pipeline_yaml, workspace_dir, COALESCE(datetime(created_at, 'localtime'), '') FROM projects WHERE name = ?
	`, name).Scan(&p.ID, &p.Name, &p.PipelineYAML, &p.WorkspaceDir, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("project %q not found (run 'deploy project load -n %s -f pipeline.yaml' first)", name, name)
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProjects 列出所有已注册项目
func (s *SqliteStore) ListProjects(ctx context.Context) ([]*Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, pipeline_yaml, workspace_dir, COALESCE(datetime(created_at, 'localtime'), '') FROM projects ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.PipelineYAML, &p.WorkspaceDir, &p.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, &p)
	}
	return list, rows.Err()
}

// DeleteProject 删除项目（先删除关联的 deployments，再删除 project）
func (s *SqliteStore) DeleteProject(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var projectID int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE name = ?", name).Scan(&projectID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("project %q not found", name)
	}
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, "DELETE FROM deployments WHERE project_id = ?", projectID)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, "DELETE FROM projects WHERE name = ?", name)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// SaveDeployment 插入部署记录（占位 RUNNING 或完成后的记录）
// paramsSnapshot 为合并后的变量 JSON，可为空时用 "{}"，引擎会在变量合并后通过 UpdateDeploymentParamsSnapshot 更新
// outputs 在插入时为空白，由 HandleEvent 完成时通过 UpdateDeploymentStatus 更新
func (s *SqliteStore) SaveDeployment(ctx context.Context, projectID int64, envName, version, status, message, infraSnapshot, pipelineSnapshot, paramsSnapshot, workspaceDir string, durationMs int64) error {
	if envName == "" {
		envName = "default"
	}
	if paramsSnapshot == "" {
		paramsSnapshot = "{}"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO deployments (project_id, env_name, version, status, message, infra_snapshot, pipeline_snapshot, params_snapshot, workspace_dir, duration_ms, outputs)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, envName, version, status, message, infraSnapshot, pipelineSnapshot, paramsSnapshot, workspaceDir, durationMs, "")
	return err
}

// UpdateDeploymentParamsSnapshot 更新部署记录的 params_snapshot（由引擎在变量合并后、执行前调用）
func (s *SqliteStore) UpdateDeploymentParamsSnapshot(ctx context.Context, projectID int64, envName, version, paramsSnapshot string) error {
	if envName == "" {
		envName = "default"
	}
	if paramsSnapshot == "" {
		paramsSnapshot = "{}"
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE deployments SET params_snapshot = ? WHERE project_id = ? AND env_name = ? AND version = ?
	`, paramsSnapshot, projectID, envName, version)
	return err
}

// UpdateDeploymentStatus 更新部署记录状态与关键产物链接（用于 HandleEvent 完成时）
func (s *SqliteStore) UpdateDeploymentStatus(ctx context.Context, projectID int64, envName, version, status, message string, durationMs int64, outputs string) error {
	if envName == "" {
		envName = "default"
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE deployments SET status = ?, message = ?, duration_ms = ?, outputs = ? WHERE project_id = ? AND env_name = ? AND version = ?
	`, status, message, durationMs, outputs, projectID, envName, version)
	return err
}

// GetDeployment 获取指定项目的指定版本部署记录（用于回滚取快照）
func (s *SqliteStore) GetDeployment(ctx context.Context, projectName, envName, version string) (*Deployment, error) {
	if envName == "" {
		envName = "default"
	}
	var d Deployment
	var startedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT d.id, d.project_id, COALESCE(d.env_name, 'default'), d.version, d.status, d.message, d.infra_snapshot, d.pipeline_snapshot, COALESCE(d.params_snapshot, '{}'), d.workspace_dir, COALESCE(d.duration_ms, 0), COALESCE(d.outputs, ''), COALESCE(datetime(d.started_at, 'localtime'), '')
		FROM deployments d
		JOIN projects p ON p.id = d.project_id
		WHERE p.name = ? AND COALESCE(d.env_name, 'default') = ? AND d.version = ?
	`, projectName, envName, version).Scan(&d.ID, &d.ProjectID, &d.EnvName, &d.Version, &d.Status, &d.Message, &d.InfraSnapshot, &d.PipelineSnapshot, &d.ParamsSnapshot, &d.WorkspaceDir, &d.DurationMs, &d.Outputs, &startedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("deployment %s@%s not found", projectName, version)
	}
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		d.StartedAt = startedAt.String
	}
	return &d, nil
}

// DeleteDeployment 删除指定 project+env+version 的部署记录
func (s *SqliteStore) DeleteDeployment(ctx context.Context, projectName, envName, version string) error {
	if envName == "" {
		envName = "default"
	}
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM deployments WHERE project_id = (SELECT id FROM projects WHERE name = ?) AND COALESCE(env_name, 'default') = ? AND version = ?
	`, projectName, envName, version)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("deployment %s@%s not found", projectName, version)
	}
	return nil
}

// GetLatestDeployment 获取项目最新已完成的部署记录（排除 RUNNING，用于 project show 等展示）
func (s *SqliteStore) GetLatestDeployment(ctx context.Context, projectName, envName string) (*Deployment, error) {
	return s.getLatestDeployment(ctx, projectName, envName, true)
}

// GetLatestDeploymentForVersionBump 获取最新部署记录用于版本自增，包含 RUNNING（避免重复版本号）
func (s *SqliteStore) GetLatestDeploymentForVersionBump(ctx context.Context, projectName, envName string) (*Deployment, error) {
	return s.getLatestDeployment(ctx, projectName, envName, false)
}

func (s *SqliteStore) getLatestDeployment(ctx context.Context, projectName, envName string, excludeRunning bool) (*Deployment, error) {
	if envName == "" {
		envName = "default"
	}
	var d Deployment
	query := `
		SELECT d.id, d.project_id, COALESCE(d.env_name, 'default'), d.version, d.status, d.message, d.infra_snapshot, d.pipeline_snapshot, COALESCE(d.params_snapshot, '{}'), d.workspace_dir, COALESCE(d.duration_ms, 0), COALESCE(d.outputs, ''), COALESCE(datetime(d.started_at, 'localtime'), '')
		FROM deployments d
		JOIN projects p ON p.id = d.project_id
		WHERE p.name = ? AND COALESCE(d.env_name, 'default') = ?
	`
	args := []interface{}{projectName, envName}
	if excludeRunning {
		query += ` AND d.status != 'RUNNING'`
	}
	query += ` ORDER BY d.started_at DESC, d.id DESC LIMIT 1`
	var startedAt sql.NullString
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&d.ID, &d.ProjectID, &d.EnvName, &d.Version, &d.Status, &d.Message, &d.InfraSnapshot, &d.PipelineSnapshot, &d.ParamsSnapshot, &d.WorkspaceDir, &d.DurationMs, &d.Outputs, &startedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		d.StartedAt = startedAt.String
	}
	return &d, nil
}

// ListEnvsForProject 返回指定项目在 deployments 中出现过的环境名列表（去重），供向导下拉使用
func (s *SqliteStore) ListEnvsForProject(ctx context.Context, projectName string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT COALESCE(d.env_name, 'default') FROM deployments d
		JOIN projects p ON p.id = d.project_id WHERE p.name = ?
		ORDER BY 1
	`, projectName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var envs []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		envs = append(envs, e)
	}
	return envs, rows.Err()
}

// ListDeployments 按 started_at 倒序获取项目的部署记录，供 history 命令使用
// envName 为空时列出所有环境；非空时仅列该环境
func (s *SqliteStore) ListDeployments(ctx context.Context, projectName, envName string, limit int) ([]*Deployment, error) {
	query := `
		SELECT d.id, d.project_id, COALESCE(d.env_name, 'default'), d.version, d.status, d.message, d.infra_snapshot, d.pipeline_snapshot, COALESCE(d.params_snapshot, '{}'), d.workspace_dir, COALESCE(d.duration_ms, 0), COALESCE(d.outputs, ''), COALESCE(datetime(d.started_at, 'localtime'), '')
		FROM deployments d
		JOIN projects p ON p.id = d.project_id
		WHERE p.name = ?
	`
	args := []interface{}{projectName}
	if envName != "" {
		query += ` AND COALESCE(d.env_name, 'default') = ?`
		args = append(args, envName)
	}
	query += ` ORDER BY d.started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Deployment
	for rows.Next() {
		var d Deployment
		var startedAt sql.NullString
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.EnvName, &d.Version, &d.Status, &d.Message, &d.InfraSnapshot, &d.PipelineSnapshot, &d.ParamsSnapshot, &d.WorkspaceDir, &d.DurationMs, &d.Outputs, &startedAt); err != nil {
			return nil, err
		}
		if startedAt.Valid {
			d.StartedAt = startedAt.String
		}
		list = append(list, &d)
	}
	return list, rows.Err()
}

// HandleEvent 实现 bus.EventHandler，监听 Pipeline 完成事件并同步更新部署记录
// 同步更新避免主流程 defer s.Close() 早于 DB 写入完成，导致状态永远停留在 RUNNING
func (s *SqliteStore) HandleEvent(e core.Event) {
	if e.Type != core.EventStatus {
		return
	}
	if e.Message != "Pipeline Completed" && e.Message != "Pipeline Failed" {
		return
	}
	if s.closed.Load() {
		return
	}

	payload, ok := e.Payload.(map[string]interface{})
	if !ok || payload == nil {
		return
	}

	projectName, _ := payload["ProjectName"].(string)
	version, _ := payload["Version"].(string)
	envName, _ := payload["EnvName"].(string)
	if projectName == "" || version == "" {
		return
	}
	if envName == "" {
		envName = "default"
	}

	status := "SUCCESS"
	if e.Message == "Pipeline Failed" {
		status = "FAILED"
	}
	msg, _ := payload["Message"].(string)
	var durationMs int64
	switch v := payload["DurationMs"].(type) {
	case int64:
		durationMs = v
	case int:
		durationMs = int64(v)
	case float64:
		durationMs = int64(v)
	}

	outputsStr := "{}"
	if m, ok := payload["Outputs"].(map[string]string); ok && len(m) > 0 {
		if b, err := json.Marshal(m); err == nil {
			outputsStr = string(b)
		}
	}

	ctx := context.Background()
	var projectID int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM projects WHERE name = ?", projectName).Scan(&projectID); err != nil {
		return
	}
	_ = s.UpdateDeploymentStatus(ctx, projectID, envName, version, status, msg, durationMs, outputsStr)
}

// Close 实现 bus.EventHandler
func (s *SqliteStore) Close() error {
	s.closed.Store(true)
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Ensure SqliteStore implements bus.EventHandler
var _ bus.EventHandler = (*SqliteStore)(nil)
