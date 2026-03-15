package core

import "path/filepath"

// GetDefaultLinkPath 返回 versions 目录对应的标准 current 软链接路径。
// 用于 release_pruner 与 symlink_switch 配置一致性的默认值。
func GetDefaultLinkPath(versionsDir string) string {
	return filepath.Join(filepath.Dir(versionsDir), "current")
}
