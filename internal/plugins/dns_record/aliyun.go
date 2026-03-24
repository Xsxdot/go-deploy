package dns_record

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Xsxdot/go-deploy/pkg/maputil"

	"github.com/aliyun/alibaba-cloud-sdk-go/services/alidns"
)

const defaultRegion = "cn-hangzhou"

// AliyunProvider 阿里云 DNS 提供商
type AliyunProvider struct {
	client *alidns.Client
}

// NewAliyunProvider 从配置创建阿里云 DNS 客户端
func NewAliyunProvider(cfg map[string]interface{}) (*AliyunProvider, error) {
	akRaw := maputil.GetString(cfg, "accessKey")
	skRaw := maputil.GetString(cfg, "accessSecret")
	ak := expandEnv(akRaw)
	sk := expandEnv(skRaw)
	if ak == "" || sk == "" {
		slog.Info("aliyun dns: credentials missing", "accessKey_raw", akRaw, "accessSecret_raw", skRaw, "ak_expanded_len", len(ak), "sk_expanded_len", len(sk), "hint", "set ALIYUN_DNS_ACCESS_KEY and ALIYUN_DNS_ACCESS_SECRET, use ${ALIYUN_DNS_ACCESS_KEY} or ${env.ALIYUN_DNS_ACCESS_KEY} in infra.yaml")
		return nil, fmt.Errorf("aliyun dns: accessKey and accessSecret are required (check env ALIYUN_DNS_ACCESS_KEY, ALIYUN_DNS_ACCESS_SECRET)")
	}
	region := maputil.GetString(cfg, "region")
	if region == "" {
		region = defaultRegion
	}
	region = expandEnv(region)

	client, err := alidns.NewClientWithAccessKey(region, ak, sk)
	if err != nil {
		return nil, fmt.Errorf("aliyun dns: create client: %w", err)
	}
	return &AliyunProvider{client: client}, nil
}

// EnsureRecords 确保域名记录指向期望的 IP 集合，缺失则添加，不匹配则更新
func (p *AliyunProvider) EnsureRecords(ctx context.Context, domain, recordType string, ips []string) error {
	if len(ips) == 0 {
		return nil
	}
	rr, domainName := parseDomain(domain)
	if rr == "" || domainName == "" {
		return fmt.Errorf("aliyun dns: invalid domain %q", domain)
	}
	if recordType == "" {
		recordType = "A"
	}

	// 查询当前记录
	req := alidns.CreateDescribeDomainRecordsRequest()
	req.Scheme = "https"
	req.DomainName = domainName
	req.RRKeyWord = rr
	req.Type = recordType

	resp, err := p.client.DescribeDomainRecords(req)
	if err != nil {
		return fmt.Errorf("aliyun dns: describe records: %w", err)
	}

	ipSet := make(map[string]bool)
	for _, ip := range ips {
		ipSet[ip] = true
	}
	used := make(map[string]bool) // 已用于更新的 recordId

	for _, rec := range resp.DomainRecords.Record {
		// 精确匹配 RR，避免 RRKeyWord 模糊匹配误删其他记录（如 tk 匹配到 tk6）
		if rec.RR != rr {
			continue
		}
		recID := rec.RecordId
		recValue := strings.TrimSpace(rec.Value)
		if recValue == "" {
			continue
		}
		if ipSet[recValue] {
			used[recValue] = true
			continue
		}
		// 记录存在但指向错误 IP，删除该记录（后续会为缺失的 IP 创建新记录）
		delReq := alidns.CreateDeleteDomainRecordRequest()
		delReq.Scheme = "https"
		delReq.RecordId = recID
		if _, err := p.client.DeleteDomainRecord(delReq); err != nil {
			return fmt.Errorf("aliyun dns: delete record %s: %w", recID, err)
		}
	}

	// 为尚未有记录的 IP 添加新记录
	for _, ip := range ips {
		if used[ip] {
			continue
		}
		addReq := alidns.CreateAddDomainRecordRequest()
		addReq.Scheme = "https"
		addReq.DomainName = domainName
		addReq.RR = rr
		addReq.Type = recordType
		addReq.Value = ip
		_, err := p.client.AddDomainRecord(addReq)
		if err != nil {
			return fmt.Errorf("aliyun dns: add record for %s: %w", ip, err)
		}
	}
	return nil
}

// DeleteRecords 删除指定域名的所有匹配记录（按 RR + recordType）
func (p *AliyunProvider) DeleteRecords(ctx context.Context, domain, recordType string) error {
	rr, domainName := parseDomain(domain)
	if rr == "" || domainName == "" {
		return fmt.Errorf("aliyun dns: invalid domain %q", domain)
	}
	if recordType == "" {
		recordType = "A"
	}

	req := alidns.CreateDescribeDomainRecordsRequest()
	req.Scheme = "https"
	req.DomainName = domainName
	req.RRKeyWord = rr
	req.Type = recordType

	resp, err := p.client.DescribeDomainRecords(req)
	if err != nil {
		return fmt.Errorf("aliyun dns: describe records: %w", err)
	}

	for _, rec := range resp.DomainRecords.Record {
		// 精确匹配 RR，避免误删其他记录
		if rec.RR != rr {
			continue
		}
		delReq := alidns.CreateDeleteDomainRecordRequest()
		delReq.Scheme = "https"
		delReq.RecordId = rec.RecordId
		_, err := p.client.DeleteDomainRecord(delReq)
		if err != nil {
			return fmt.Errorf("aliyun dns: delete record %s: %w", rec.RecordId, err)
		}
	}
	return nil
}
