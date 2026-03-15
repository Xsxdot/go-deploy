package dns_record

import (
	"context"
	"fmt"
	"strings"

	"github.com/Xsxdot/go-deploy/pkg/maputil"

	"github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/dns"
	"github.com/cloudflare/cloudflare-go/v6/option"
	"github.com/cloudflare/cloudflare-go/v6/zones"
)

// CloudflareProvider Cloudflare DNS 提供商
type CloudflareProvider struct {
	client  *cloudflare.Client
	zoneID  string
	proxied bool // true=代理(橙云)，false=仅DNS(灰云)
}

// NewCloudflareProvider 从配置创建 Cloudflare DNS 客户端
func NewCloudflareProvider(cfg map[string]interface{}) (*CloudflareProvider, error) {
	token := expandEnv(maputil.GetString(cfg, "apiToken"))
	if token == "" {
		zoneID := expandEnv(maputil.GetString(cfg, "zoneId"))
		email := expandEnv(maputil.GetString(cfg, "email"))
		apiKey := expandEnv(maputil.GetString(cfg, "apiKey"))
		if zoneID == "" || email == "" || apiKey == "" {
			return nil, fmt.Errorf("cloudflare dns: apiToken or (zoneId+email+apiKey) required")
		}
		// Legacy auth - still need token for v6 SDK, try apiToken first
		return nil, fmt.Errorf("cloudflare dns: apiToken is required (legacy apiKey+email not supported by v6)")
	}

	client := cloudflare.NewClient(option.WithAPIToken(token))
	p := &CloudflareProvider{
		client:  client,
		zoneID:  expandEnv(maputil.GetString(cfg, "zoneId")),
		proxied: maputil.GetBool(cfg, "proxied"),
	}
	return p, nil
}

// resolveZoneID 通过域名查找 Zone ID（当未配置 zoneId 时）
func (p *CloudflareProvider) resolveZoneID(ctx context.Context, domain string) (string, error) {
	if p.zoneID != "" {
		return p.zoneID, nil
	}
	_, domainName := parseDomain(domain)
	if domainName == "" {
		domainName = domain
	}

	page, err := p.client.Zones.List(ctx, zones.ZoneListParams{
		Name: cloudflare.F(domainName),
	})
	if err != nil {
		return "", fmt.Errorf("cloudflare dns: list zones: %w", err)
	}
	if len(page.Result) == 0 {
		return "", fmt.Errorf("cloudflare dns: zone not found for domain %q", domainName)
	}
	return page.Result[0].ID, nil
}

// EnsureRecords 确保域名记录指向期望的 IP 集合，缺失则添加，不匹配则更新
func (p *CloudflareProvider) EnsureRecords(ctx context.Context, domain, recordType string, ips []string) error {
	if len(ips) == 0 {
		return nil
	}
	if recordType == "" {
		recordType = "A"
	}

	zoneID, err := p.resolveZoneID(ctx, domain)
	if err != nil {
		return err
	}

	// List existing records for this name and type
	iter := p.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cloudflare.F(zoneID),
		Name:   cloudflare.F(dns.RecordListParamsName{Exact: cloudflare.F(domain)}),
		Type:   cloudflare.F(dns.RecordListParamsType(recordType)),
	})

	ipSet := make(map[string]bool)
	for _, ip := range ips {
		ipSet[ip] = true
	}
	satisfied := make(map[string]bool)
	var wrongRecords []dns.RecordResponse

	for iter.Next() {
		rec := iter.Current()
		content := strings.TrimSpace(rec.Content)
		if content == "" {
			continue
		}
		if ipSet[content] {
			satisfied[content] = true
			continue
		}
		wrongRecords = append(wrongRecords, rec)
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("cloudflare dns: list records: %w", err)
	}

	// Repurpose wrong records to point to unsatisfied IPs
	for _, rec := range wrongRecords {
		var targetIP string
		for ip := range ipSet {
			if !satisfied[ip] {
				targetIP = ip
				break
			}
		}
		if targetIP == "" {
			break
		}
		var body dns.RecordUpdateParamsBodyUnion
		if recordType == "A" {
			body = dns.ARecordParam{
				Name:    cloudflare.F(domain),
				Type:    cloudflare.F(dns.ARecordTypeA),
				Content: cloudflare.F(targetIP),
				TTL:     cloudflare.F(dns.TTL1),
				Proxied: cloudflare.F(p.proxied),
			}
		} else if recordType == "AAAA" {
			body = dns.AAAARecordParam{
				Name:    cloudflare.F(domain),
				Type:    cloudflare.F(dns.AAAARecordTypeAAAA),
				Content: cloudflare.F(targetIP),
				TTL:     cloudflare.F(dns.TTL1),
				Proxied: cloudflare.F(p.proxied),
			}
		} else {
			return fmt.Errorf("cloudflare dns: unsupported record type %q", recordType)
		}
		_, err := p.client.DNS.Records.Update(ctx, rec.ID, dns.RecordUpdateParams{
			ZoneID: cloudflare.F(zoneID),
			Body:   body,
		})
		if err != nil {
			return fmt.Errorf("cloudflare dns: update record %s: %w", rec.ID, err)
		}
		satisfied[targetIP] = true
	}

	// Add new records for unsatisfied IPs
	for _, ip := range ips {
		if satisfied[ip] {
			continue
		}
		var body dns.RecordNewParamsBodyUnion
		if recordType == "A" {
			body = dns.ARecordParam{
				Name:    cloudflare.F(domain),
				Type:    cloudflare.F(dns.ARecordTypeA),
				Content: cloudflare.F(ip),
				TTL:     cloudflare.F(dns.TTL1),
				Proxied: cloudflare.F(p.proxied),
			}
		} else if recordType == "AAAA" {
			body = dns.AAAARecordParam{
				Name:    cloudflare.F(domain),
				Type:    cloudflare.F(dns.AAAARecordTypeAAAA),
				Content: cloudflare.F(ip),
				TTL:     cloudflare.F(dns.TTL1),
				Proxied: cloudflare.F(p.proxied),
			}
		} else {
			return fmt.Errorf("cloudflare dns: unsupported record type %q", recordType)
		}
		_, err := p.client.DNS.Records.New(ctx, dns.RecordNewParams{
			ZoneID: cloudflare.F(zoneID),
			Body:   body,
		})
		if err != nil {
			return fmt.Errorf("cloudflare dns: add record for %s: %w", ip, err)
		}
	}
	return nil
}

// DeleteRecords 删除指定域名的所有匹配记录（按 name + type）
func (p *CloudflareProvider) DeleteRecords(ctx context.Context, domain, recordType string) error {
	if recordType == "" {
		recordType = "A"
	}

	zoneID, err := p.resolveZoneID(ctx, domain)
	if err != nil {
		return err
	}

	iter := p.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cloudflare.F(zoneID),
		Name:   cloudflare.F(dns.RecordListParamsName{Exact: cloudflare.F(domain)}),
		Type:   cloudflare.F(dns.RecordListParamsType(recordType)),
	})

	for iter.Next() {
		rec := iter.Current()
		_, err := p.client.DNS.Records.Delete(ctx, rec.ID, dns.RecordDeleteParams{
			ZoneID: cloudflare.F(zoneID),
		})
		if err != nil {
			return fmt.Errorf("cloudflare dns: delete record %s: %w", rec.ID, err)
		}
	}
	return iter.Err()
}
