package sshutil

import (
	"github.com/Xsxdot/go-deploy/internal/core"
)

// AsHostTarget 将 Target 断言为 *core.HostTarget，非 HostTarget 时返回 (nil, false)
func AsHostTarget(t core.Target) (*core.HostTarget, bool) {
	if t == nil {
		return nil, false
	}
	host, ok := t.(*core.HostTarget)
	return host, ok
}

// IsHostTarget 判断 Target 是否为 HostTarget
func IsHostTarget(t core.Target) bool {
	_, ok := AsHostTarget(t)
	return ok
}
