package store

// Schema DDL for deploy SQLite database (no backward compatibility; drop old db if upgrading)
const Schema = `
CREATE TABLE IF NOT EXISTS global_infra (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    infra_yaml TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    pipeline_yaml TEXT NOT NULL,
    workspace_dir TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS deployments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL,
    env_name TEXT NOT NULL DEFAULT 'default',
    version TEXT NOT NULL,
    status TEXT NOT NULL,
    message TEXT,
    infra_snapshot TEXT NOT NULL,
    pipeline_snapshot TEXT NOT NULL,
    params_snapshot TEXT NOT NULL DEFAULT '{}',
    workspace_dir TEXT NOT NULL,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    duration_ms INTEGER,
    outputs TEXT,
    FOREIGN KEY(project_id) REFERENCES projects(id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_deployments_project_env_version ON deployments(project_id, env_name, version);
CREATE INDEX IF NOT EXISTS idx_deployments_project ON deployments(project_id);
`
