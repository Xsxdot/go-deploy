package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Xsxdot/go-deploy/internal/core"

	"gopkg.in/yaml.v3"
)

// ParsePipelineBytes 从字节解析 pipeline 配置（供 SQLite 快照反序列化）。
// baseDir 用于解析 include 步骤中模板的相对路径，为空时则按模板路径原样读取。
func ParsePipelineBytes(data []byte, baseDir string) (*core.PipelineConfig, error) {
	var cfg core.PipelineConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		slog.Error("解析 pipeline YAML 失败", "err", err)
		return nil, fmt.Errorf("parse pipeline yaml: %w", err)
	}
	// 宏展开：在校验 DAG 之前，将 include 步骤就地打平
	expanded, err := ExpandPipeline(&cfg.Pipeline, baseDir)
	if err != nil {
		slog.Error("宏展开失败", "err", err)
		return nil, fmt.Errorf("macro expansion failed: %w", err)
	}
	cfg.Pipeline = *expanded
	return &cfg, nil
}

// infraRaw 用于解析 infra YAML 的中间结构，同时支持旧格式 hosts list 与新格式 resources map
type infraRaw struct {
	Providers  map[string]map[string]map[string]interface{} `yaml:"providers"`
	GlobalVars map[string]string                            `yaml:"globalVars"`
	Resources  map[string]*core.HostTarget                  `yaml:"resources"` // 新格式：resources map
	Hosts      []struct {                                    // 旧格式：hosts list（向后兼容）
		ID            string            `yaml:"id"`
		Addr          string            `yaml:"addr"`
		LanAddr       string            `yaml:"lanAddr,omitempty"`
		PublicAddr    string            `yaml:"publicAddr,omitempty"`
		User          string            `yaml:"user"`
		Auth          map[string]string `yaml:"auth"`
		Proxy         string            `yaml:"proxy,omitempty"`
		Bastion       string            `yaml:"bastion,omitempty"`
		HasNginx      bool              `yaml:"hasNginx,omitempty"`
		NginxConfPath string            `yaml:"nginxConfPath,omitempty"`
	} `yaml:"hosts"`
	Roles map[string][]string `yaml:"roles"`
}

// ParseInfraBytes 从字节解析 infra 配置（供 SQLite 快照反序列化）
// 同时支持新格式 resources map 与旧格式 hosts list，两者可共存，resources 优先
func ParseInfraBytes(data []byte) (*core.InfraConfig, error) {
	var raw infraRaw
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse infra yaml: %w", err)
	}

	cfg := &core.InfraConfig{
		Providers:  raw.Providers,
		GlobalVars: raw.GlobalVars,
		Roles:      raw.Roles,
		Resources:  make(map[string]core.Target),
	}

	// 迁移旧格式 hosts list -> Resources map
	for _, h := range raw.Hosts {
		if h.ID == "" {
			continue
		}
		cfg.Resources[h.ID] = &core.HostTarget{
			ResourceID:    h.ID,
			Addr:          h.Addr,
			LanAddr:       h.LanAddr,
			PublicAddr:    h.PublicAddr,
			User:          h.User,
			Auth:          h.Auth,
			Proxy:         h.Proxy,
			Bastion:       h.Bastion,
			HasNginx:      h.HasNginx,
			NginxConfPath: h.NginxConfPath,
		}
	}

	// 合并新格式 resources map（新格式优先，同名覆盖旧格式）
	for id, h := range raw.Resources {
		if h == nil {
			continue
		}
		h.ResourceID = id
		cfg.Resources[id] = h
	}

	return cfg, nil
}

// LoadPipeline 从文件路径加载 pipeline 配置
func LoadPipeline(path string) (*core.PipelineConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("读取 pipeline 文件失败", "path", path, "err", err)
		return nil, fmt.Errorf("read pipeline file: %w", err)
	}
	return ParsePipelineBytes(data, filepath.Dir(path))
}

// LoadInfra 从文件路径加载 infra 配置
func LoadInfra(path string) (*core.InfraConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("读取 infra 文件失败", "path", path, "err", err)
		return nil, fmt.Errorf("read infra file: %w", err)
	}
	return ParseInfraBytes(data)
}
