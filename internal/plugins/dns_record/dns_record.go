package dns_record

import (
	"context"
	"fmt"
	"strings"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/internal/engine"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
)

// DnsRecordPlugin 域名管理插件，支持阿里云和 Cloudflare
type DnsRecordPlugin struct{}

// NewDnsRecordPlugin 创建 dns_record 插件实例
func NewDnsRecordPlugin() *DnsRecordPlugin {
	return &DnsRecordPlugin{}
}

// Name 实现 StepPlugin
func (p *DnsRecordPlugin) Name() string {
	return "dns_record"
}

// Execute 实现 StepPlugin
func (p *DnsRecordPlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	providerName := maputil.GetString(step.With, "provider")
	domain := maputil.GetString(step.With, "domain")
	recordType := maputil.GetString(step.With, "record_type")
	targetRoles := maputil.GetStringSlice(step.With, "target_roles")

	if providerName == "" {
		ctx.LogError(step.Name, "", "provider is required")
		return fmt.Errorf("dns_record: provider is required")
	}
	if domain == "" {
		ctx.LogError(step.Name, "", "domain is required")
		return fmt.Errorf("dns_record: domain is required")
	}
	if len(targetRoles) == 0 {
		ctx.LogError(step.Name, "", "target_roles is required")
		return fmt.Errorf("dns_record: target_roles is required")
	}
	if recordType == "" {
		recordType = "A"
	}

	if ctx.Render != nil {
		domain = ctx.Render(domain)
		providerName = ctx.Render(providerName)
	}

	// 使用 target_roles 解析 targets（而非 step.Roles）
	resolved, err := engine.ResolveTargets(targetRoles, ctx.Infra)
	if err != nil {
		ctx.LogError(step.Name, "", fmt.Sprintf("resolve targets: %v", err))
		return fmt.Errorf("dns_record: resolve targets: %w", err)
	}

	ips := collectIPs(resolved)
	if len(ips) == 0 {
		ctx.LogError(step.Name, "", fmt.Sprintf("no valid IPs from target_roles %v", targetRoles))
		return fmt.Errorf("dns_record: no valid IPs from target_roles %v (check publicAddr or addr on hosts)", targetRoles)
	}

	dnsProviders, ok := ctx.Infra.Providers["dns"]
	if !ok || dnsProviders == nil {
		ctx.LogError(step.Name, "", "providers.dns not found in infra")
		return fmt.Errorf("dns_record: providers.dns not found in infra")
	}
	providerCfg, ok := dnsProviders[providerName]
	if !ok || providerCfg == nil {
		ctx.LogError(step.Name, "", fmt.Sprintf("provider %q not found in providers.dns", providerName))
		return fmt.Errorf("dns_record: provider %q not found in providers.dns", providerName)
	}

	providerType := maputil.GetString(providerCfg, "type")
	if providerType == "" {
		providerType = "aliyun" // 兼容旧配置
	}
	if ctx.Render != nil {
		providerType = ctx.Render(providerType)
	}

	var provider interface {
		EnsureRecords(ctx context.Context, domain, recordType string, ips []string) error
	}

	switch providerType {
	case "aliyun":
		provider, err = NewAliyunProvider(providerCfg)
	case "cloudflare":
		provider, err = NewCloudflareProvider(providerCfg)
	default:
		ctx.LogError(step.Name, "", fmt.Sprintf("unsupported provider type %q", providerType))
		return fmt.Errorf("dns_record: unsupported provider type %q (aliyun, cloudflare)", providerType)
	}
	if err != nil {
		ctx.LogError(step.Name, "", fmt.Sprintf("create provider: %v", err))
		return fmt.Errorf("dns_record: create provider: %w", err)
	}

	ctx.LogInfo(step.Name, "", fmt.Sprintf("Updating DNS %s %s -> %s", domain, recordType, strings.Join(ips, ", ")))

	err = provider.EnsureRecords(ctx, domain, recordType, ips)
	if err != nil {
		ctx.LogError(step.Name, "", fmt.Sprintf("EnsureRecords failed: %v", err))
		return err
	}
	ctx.LogInfo(step.Name, "", "DNS records updated")
	return nil
}

// Rollback 实现 StepPlugin，因 DNS 有缓存机制，仅打印告警，不做激进删除
func (p *DnsRecordPlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	ctx.LogWarn(step.Name, "", "Rollback skipped (DNS has cache, no aggressive delete)")
	return nil
}

// dnsProviderWithDelete 支持 DeleteRecords 的 DNS 提供商接口
type dnsProviderWithDelete interface {
	EnsureRecords(ctx context.Context, domain, recordType string, ips []string) error
	DeleteRecords(ctx context.Context, domain, recordType string) error
}

// Uninstall 实现 StepPlugin，当 ctx.FullPurge 时调用 Provider.DeleteRecords 删除 DNS 记录
func (p *DnsRecordPlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	if !ctx.FullPurge {
		ctx.LogInfo(step.Name, "", "Uninstall skipped (use --full to delete DNS records)")
		return nil
	}

	providerName := maputil.GetString(step.With, "provider")
	domain := maputil.GetString(step.With, "domain")
	recordType := maputil.GetString(step.With, "record_type")
	if providerName == "" || domain == "" {
		ctx.LogInfo(step.Name, "", "Uninstall skipped (provider or domain missing)")
		return nil
	}
	if recordType == "" {
		recordType = "A"
	}
	if ctx.Render != nil {
		domain = ctx.Render(domain)
		providerName = ctx.Render(providerName)
	}

	dnsProviders, ok := ctx.Infra.Providers["dns"]
	if !ok || dnsProviders == nil {
		ctx.LogInfo(step.Name, "", "Uninstall skipped (providers.dns not found)")
		return nil
	}
	providerCfg, ok := dnsProviders[providerName]
	if !ok || providerCfg == nil {
		ctx.LogInfo(step.Name, "", fmt.Sprintf("Uninstall skipped (provider %q not found)", providerName))
		return nil
	}

	providerType := maputil.GetString(providerCfg, "type")
	if providerType == "" {
		providerType = "aliyun"
	}
	if ctx.Render != nil {
		providerType = ctx.Render(providerType)
	}

	var provider interface {
		EnsureRecords(ctx context.Context, domain, recordType string, ips []string) error
	}
	var err error
	switch providerType {
	case "aliyun":
		provider, err = NewAliyunProvider(providerCfg)
	case "cloudflare":
		provider, err = NewCloudflareProvider(providerCfg)
	default:
		ctx.LogInfo(step.Name, "", fmt.Sprintf("Uninstall skipped (unsupported provider %q)", providerType))
		return nil
	}
	if err != nil {
		return fmt.Errorf("dns_record: create provider: %w", err)
	}

	delProvider, ok := provider.(dnsProviderWithDelete)
	if !ok {
		ctx.LogInfo(step.Name, "", "Uninstall skipped (provider does not support DeleteRecords)")
		return nil
	}

	ctx.LogInfo(step.Name, "", fmt.Sprintf("Deleting DNS records for %s (%s)", domain, recordType))
	if err := delProvider.DeleteRecords(ctx, domain, recordType); err != nil {
		return fmt.Errorf("dns_record: DeleteRecords failed: %w", err)
	}
	ctx.LogInfo(step.Name, "", "DNS records deleted")
	return nil
}
