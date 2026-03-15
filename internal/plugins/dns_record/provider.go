package dns_record

import (
	"net"
	"os"
	"regexp"
	"strings"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/sshutil"
)

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandEnv 将字符串中的 ${VAR} 或 ${env.VAR} 替换为 os.Getenv("VAR")
// 支持 env. 前缀：${env.ALIYUN_DNS_ACCESS_KEY} 会查找环境变量 ALIYUN_DNS_ACCESS_KEY
func expandEnv(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		key := strings.TrimPrefix(strings.TrimSuffix(match, "}"), "${")
		// ${env.XXX} 约定：实际查找环境变量 XXX，避免与字面量 env.XXX 混淆
		if strings.HasPrefix(key, "env.") {
			key = strings.TrimPrefix(key, "env.")
		}
		return os.Getenv(key)
	})
}

// parseDomain 将完整域名解析为 RR 和根域名，如 api.example.com -> RR=api, DomainName=example.com；apex 如 example.com -> RR="", DomainName=example.com
func parseDomain(domain string) (rr, domainName string) {
	idx := strings.Index(domain, ".")
	if idx < 0 {
		return "", domain
	}
	// 仅一个点的 apex 域名（如 example.com）保持完整
	if strings.Count(domain, ".") == 1 {
		return "", domain
	}
	return domain[:idx], domain[idx+1:]
}

// collectIPs 从 targets 中提取公网 IP（优先 PublicAddr，否则 Addr），去重并校验为有效 IP
func collectIPs(targets []core.Target) []string {
	seen := make(map[string]bool)
	var ips []string
	for _, t := range targets {
		host, ok := sshutil.AsHostTarget(t)
		if !ok || host == nil {
			continue
		}
		addr := host.PublicAddr
		if addr == "" {
			addr = host.Addr
		}
		addr = strings.TrimSpace(addr)
		if addr == "" || seen[addr] {
			continue
		}
		if net.ParseIP(addr) == nil {
			continue
		}
		seen[addr] = true
		ips = append(ips, addr)
	}
	return ips
}
