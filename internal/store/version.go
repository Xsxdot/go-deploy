package store

import (
	"fmt"
	"strconv"
	"strings"
)

// BumpVersion 根据 bump 类型递增版本号，支持 major / minor / patch
// current 格式为 v1.2.3 或 1.2.3；无当前版本时返回 v1.0.0
func BumpVersion(current, bump string) (string, error) {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	if current == "" {
		return "v1.0.0", nil
	}

	parts := strings.Split(current, ".")
	major, minor, patch := 1, 0, 0
	if len(parts) >= 1 && parts[0] != "" {
		if v, err := strconv.Atoi(parts[0]); err == nil {
			major = v
		}
	}
	if len(parts) >= 2 && parts[1] != "" {
		if v, err := strconv.Atoi(parts[1]); err == nil {
			minor = v
		}
	}
	if len(parts) >= 3 && parts[2] != "" {
		if v, err := strconv.Atoi(parts[2]); err == nil {
			patch = v
		}
	}

	switch strings.ToLower(bump) {
	case "major":
		major++
		minor = 0
		patch = 0
	case "minor":
		minor++
		patch = 0
	case "patch":
		patch++
	default:
		return "", fmt.Errorf("invalid bump type %q (use major, minor, or patch)", bump)
	}

	return fmt.Sprintf("v%d.%d.%d", major, minor, patch), nil
}
