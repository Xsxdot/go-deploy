package dns_record

import (
	"context"
	"os"
	"testing"

	"github.com/Xsxdot/go-deploy/internal/core"
)

// === 请填写以下数据后运行: go test -v -run Integration ./internal/plugins/dns_record ===
//
// 可从常量填写，或通过环境变量：export ALIYUN_AK=xxx ALIYUN_SK=xxx / CF_API_TOKEN=xxx
var (
	testAliyunAccessKey    = ""               // 或 os.Getenv("ALIYUN_AK")
	testAliyunAccessSecret = ""               // 或 os.Getenv("ALIYUN_SK")
	testCFApiToken         = ""               // 或 os.Getenv("CF_API_TOKEN")
	testCFZoneId           = ""               // 可选，不填时按 domain 自动解析
	testDomain             = "temp.cl360.xyz" // 如 api.example.com
	testIP                 = "47.85.3.239"    // 如 1.2.3.4，将写入 A 记录
)

func TestParseDomain(t *testing.T) {
	tests := []struct {
		domain     string
		wantRR     string
		wantDomain string
	}{
		{"api.example.com", "api", "example.com"},
		{"gateway.api.example.com", "gateway", "api.example.com"},
		{"example.com", "", "example.com"},
		{"a", "", "a"},
	}
	for _, tt := range tests {
		rr, domainName := parseDomain(tt.domain)
		if rr != tt.wantRR || domainName != tt.wantDomain {
			t.Errorf("parseDomain(%q) = (%q, %q), want (%q, %q)", tt.domain, rr, domainName, tt.wantRR, tt.wantDomain)
		}
	}
}

func TestCollectIPs(t *testing.T) {
	targets := []core.Target{
		&core.HostTarget{ResourceID: "h1", Addr: "192.168.1.1", PublicAddr: "203.0.113.10"},
		&core.HostTarget{ResourceID: "h2", Addr: "192.168.1.2"},                      // no PublicAddr, use Addr
		&core.HostTarget{ResourceID: "h3", Addr: "invalid"},                          // skip invalid IP
		&core.HostTarget{ResourceID: "h4", Addr: "10.0.0.1", PublicAddr: "10.0.0.1"}, // duplicate
	}
	ips := collectIPs(targets)
	if len(ips) != 3 { // 203.0.113.10, 192.168.1.2, 10.0.0.1 (invalid skipped, duplicates by design - h4 same as... no h3 has invalid so we have 3 valid: 203.0.113.10, 192.168.1.2, 10.0.0.1)
		t.Errorf("collectIPs: got %d IPs, want 3: %v", len(ips), ips)
	}
	seen := make(map[string]bool)
	for _, ip := range ips {
		if seen[ip] {
			t.Errorf("collectIPs: duplicate IP %s", ip)
		}
		seen[ip] = true
	}
	if !seen["203.0.113.10"] || !seen["192.168.1.2"] || !seen["10.0.0.1"] {
		t.Errorf("collectIPs: missing expected IPs, got %v", ips)
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("TEST_VAR", "myvalue")
	got := expandEnv("prefix_${TEST_VAR}_suffix")
	if got != "prefix_myvalue_suffix" {
		t.Errorf("expandEnv: got %q, want prefix_myvalue_suffix", got)
	}
}

// getTestOrEnv 优先使用常量，为空时从环境变量读取（便于 CI/本地不提交敏感信息）
func getTestOrEnv(val, envKey string) string {
	if val != "" {
		return val
	}
	return os.Getenv(envKey)
}

func TestExecute_Aliyun_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ak := getTestOrEnv(testAliyunAccessKey, "ALIYUN_DNS_ACCESS_KEY")
	sk := getTestOrEnv(testAliyunAccessSecret, "ALIYUN_DNS_ACCESS_SECRET")
	if testDomain == "" || testIP == "" || ak == "" || sk == "" {
		t.Skip("请填写 testDomain、testIP 及阿里云 accessKey/accessSecret（或设置 ALIYUN_AK、ALIYUN_SK 环境变量）")
	}
	infra := &core.InfraConfig{
		Providers: map[string]map[string]map[string]interface{}{
			"dns": {
				"aliyun-test": {
					"type":         "aliyun",
					"accessKey":    ak,
					"accessSecret": sk,
				},
			},
		},
		Roles: map[string][]string{"servers": {"h1"}},
		Resources: map[string]core.Target{
			"h1": &core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1", PublicAddr: testIP},
		},
	}
	step := core.Step{
		Name: "dns-test", Type: "dns_record",
		With: map[string]interface{}{
			"provider":     "aliyun-test",
			"domain":       testDomain,
			"record_type":  "A",
			"target_roles": []string{"servers"},
		},
	}
	ctx := core.NewDeployContext(context.Background(), infra, nil, nil, nil, "")
	defer ctx.Cancel()

	plugin := NewDnsRecordPlugin()
	if err := plugin.Execute(ctx, step, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestExecute_Cloudflare_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	token := getTestOrEnv(testCFApiToken, "CLOUDFLARE_API_TOKEN")
	if testDomain == "" || testIP == "" || token == "" {
		t.Skip("请填写 testDomain、testIP 及 Cloudflare apiToken（或设置 CF_API_TOKEN 环境变量）")
	}
	cfg := map[string]interface{}{
		"type":     "cloudflare",
		"apiToken": token,
	}
	if testCFZoneId != "" {
		cfg["zoneId"] = testCFZoneId
	}
	infra := &core.InfraConfig{
		Providers: map[string]map[string]map[string]interface{}{
			"dns": {"cf-test": cfg},
		},
		Roles: map[string][]string{"servers": {"h1"}},
		Resources: map[string]core.Target{
			"h1": &core.HostTarget{ResourceID: "h1", Addr: "127.0.0.1", PublicAddr: testIP},
		},
	}
	step := core.Step{
		Name: "dns-test", Type: "dns_record",
		With: map[string]interface{}{
			"provider":     "cf-test",
			"domain":       testDomain,
			"record_type":  "A",
			"target_roles": []string{"servers"},
		},
	}
	ctx := core.NewDeployContext(context.Background(), infra, nil, nil, nil, "")
	defer ctx.Cancel()

	plugin := NewDnsRecordPlugin()
	if err := plugin.Execute(ctx, step, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}
