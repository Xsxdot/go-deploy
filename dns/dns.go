package dns

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"deploy/executor"
	"deploy/model"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// CheckAndCreateDNSRecord 检查并创建 DNS 记录
func CheckAndCreateDNSRecord(service *model.Service, env model.Env, serverHosts []string, accessKey, accessSecret string) error {
	envConfig := service.TestEnv
	if env == model.Prod {
		envConfig = service.ProdEnv
	}

	if envConfig == nil || envConfig.Domain == "" {
		fmt.Println("跳过 DNS 配置 - 未配置域名")
		return nil
	}

	domain := envConfig.Domain
	// 解析主域名和子域名
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return fmt.Errorf("域名格式不正确: %s", domain)
	}

	var recordName string
	var mainDomain string

	if len(parts) == 2 {
		// 如果是 example.com，则 recordName 为 @
		recordName = "@"
		mainDomain = domain
	} else {
		// 如果是 api.example.com，则 recordName 为 api，mainDomain 为 example.com
		recordName = strings.Join(parts[:len(parts)-2], ".")
		mainDomain = strings.Join(parts[len(parts)-2:], ".")
	}

	fmt.Printf("检查 DNS 记录: %s (主域名: %s, 记录名: %s)\n", domain, mainDomain, recordName)

	// 检查现有的 DNS 记录
	existingRecords, err := getDNSRecords(mainDomain, accessKey, accessSecret)
	if err != nil {
		return fmt.Errorf("获取 DNS 记录失败: %w", err)
	}

	// 检查记录是否已存在
	for _, record := range existingRecords {
		if record.RR == recordName && record.Type == "A" {
			fmt.Println("DNS 记录已存在，跳过创建")
			return nil
		}
	}

	// 记录不存在，创建新记录
	fmt.Printf("DNS 记录不存在，创建新记录: %s\n", domain)
	return CreateOrUpdateDNSRecord(service, env, serverHosts, accessKey, accessSecret)
}

// CreateOrUpdateDNSRecord 强制创建或更新 DNS 记录
func CreateOrUpdateDNSRecord(service *model.Service, env model.Env, serverHosts []string, accessKey, accessSecret string) error {
	envConfig := service.TestEnv
	if env == model.Prod {
		envConfig = service.ProdEnv
	}

	if envConfig == nil || envConfig.Domain == "" {
		fmt.Println("跳过 DNS 配置 - 未配置域名")
		return nil
	}

	domain := envConfig.Domain
	// 解析主域名和子域名
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return fmt.Errorf("域名格式不正确: %s", domain)
	}

	var recordName string
	var mainDomain string

	if len(parts) == 2 {
		// 如果是 example.com，则 recordName 为 @
		recordName = "@"
		mainDomain = domain
	} else {
		// 如果是 api.example.com，则 recordName 为 api，mainDomain 为 example.com
		recordName = strings.Join(parts[:len(parts)-2], ".")
		mainDomain = strings.Join(parts[len(parts)-2:], ".")
	}

	fmt.Printf("强制创建或更新 DNS 记录: %s (主域名: %s, 记录名: %s)\n", domain, mainDomain, recordName)

	// 检查现有的 DNS 记录
	existingRecords, err := getDNSRecords(mainDomain, accessKey, accessSecret)
	if err != nil {
		return fmt.Errorf("获取 DNS 记录失败: %w", err)
	}

	// 获取服务器的公网IP
	publicIPs, err := getPublicIPs(serverHosts)
	if err != nil {
		return fmt.Errorf("获取公网IP失败: %w", err)
	}

	fmt.Printf("服务器公网IP: %v\n", publicIPs)

	// 查找现有记录
	var existingRecord *DNSRecord
	for _, record := range existingRecords {
		if record.RR == recordName && record.Type == "A" {
			existingRecord = &record
			break
		}
	}

	if existingRecord == nil {
		// 记录不存在，创建新记录
		fmt.Printf("创建新 DNS 记录: %s -> %v\n", domain, publicIPs)
		err = addDNSRecord(mainDomain, recordName, "A", strings.Join(publicIPs, ","), accessKey, accessSecret)
		if err != nil {
			return fmt.Errorf("创建 DNS 记录失败: %w", err)
		}
		fmt.Println("DNS 记录创建成功")
	} else {
		// 记录存在，检查是否需要更新
		if containsAllIPs(existingRecord.Value, publicIPs) {
			fmt.Printf("DNS 记录无变动，跳过更新: %s\n", domain)
			return nil
		}

		// 记录需要更新
		fmt.Printf("更新 DNS 记录: %s -> %v (原值: %s)\n", domain, publicIPs, existingRecord.Value)
		err = updateDNSRecord(existingRecord.RecordId, "A", recordName, strings.Join(publicIPs, ","), accessKey, accessSecret)
		if err != nil {
			return fmt.Errorf("更新 DNS 记录失败: %w", err)
		}
		fmt.Println("DNS 记录更新成功")
	}

	return nil
}

// getPublicIPs 获取服务器的公网IP
func getPublicIPs(serverHosts []string) ([]string, error) {
	var publicIPs []string
	exectr := executor.NewExecutor()

	for _, host := range serverHosts {
		publicIP, err := getPublicIPFromServer(exectr, host)
		if err != nil {
			fmt.Printf("警告: 无法获取服务器 %s 的公网IP: %v，使用原始IP\n", host, err)
			publicIPs = append(publicIPs, host)
		} else {
			publicIPs = append(publicIPs, publicIP)
		}
	}

	return publicIPs, nil
}

// getPublicIPFromServer 从指定服务器获取公网IP
func getPublicIPFromServer(exectr *executor.Executor, serverHost string) (string, error) {
	ctx := context.Background()

	// 创建获取公网IP的命令，使用多个备选服务（优先使用国内稳定的服务）
	cmd := &executor.Command{
		ID:      "get_public_ip",
		Name:    "获取公网IP",
		Command: "curl -s --connect-timeout 10 http://ip.sb || curl -s --connect-timeout 10 http://ifconfig.me ||  curl -s --connect-timeout 10 http://icanhazip.com || curl -s --connect-timeout 10 http://ident.me",
		Timeout: 30 * time.Second,
	}

	// 创建执行请求
	req := &executor.ExecuteRequest{
		Type:       executor.CommandTypeSingle,
		ServerHost: serverHost,
		Command:    cmd,
	}

	// 执行命令
	result, err := exectr.Execute(ctx, req)
	if err != nil {
		return "", fmt.Errorf("执行获取公网IP命令失败: %w", err)
	}

	if result.CommandResult.Status != executor.CommandStatusSuccess {
		return "", fmt.Errorf("获取公网IP命令执行失败: %s", result.CommandResult.Error)
	}

	// 解析公网IP
	publicIP := strings.TrimSpace(result.CommandResult.Stdout)
	if publicIP == "" {
		return "", fmt.Errorf("未能获取到公网IP")
	}

	// 简单验证IP格式
	parts := strings.Split(publicIP, ".")
	if len(parts) != 4 {
		return "", fmt.Errorf("获取到的公网IP格式不正确: %s", publicIP)
	}

	fmt.Printf("服务器 %s 的公网IP: %s\n", serverHost, publicIP)
	return publicIP, nil
}

// DNSRecord DNS 记录结构
type DNSRecord struct {
	RecordId string `json:"RecordId"`
	RR       string `json:"RR"`
	Type     string `json:"Type"`
	Value    string `json:"Value"`
	TTL      int    `json:"TTL"`
}

// DNSResponse 阿里云 DNS API 响应
type DNSResponse struct {
	DomainRecords struct {
		Record []DNSRecord `json:"Record"`
	} `json:"DomainRecords"`
}

// getDNSRecords 获取域名的 DNS 记录
func getDNSRecords(domain, accessKey, accessSecret string) ([]DNSRecord, error) {
	params := map[string]string{
		"Action":     "DescribeDomainRecords",
		"DomainName": domain,
		"Format":     "JSON",
		"Version":    "2015-01-09",
	}

	resp, err := makeAliDNSRequest(params, accessKey, accessSecret)
	if err != nil {
		return nil, err
	}

	var dnsResp DNSResponse
	err = json.Unmarshal(resp, &dnsResp)
	if err != nil {
		return nil, fmt.Errorf("解析 DNS 响应失败: %w", err)
	}

	return dnsResp.DomainRecords.Record, nil
}

// addDNSRecord 添加 DNS 记录
func addDNSRecord(domain, rr, recordType, value, accessKey, accessSecret string) error {
	params := map[string]string{
		"Action":     "AddDomainRecord",
		"DomainName": domain,
		"RR":         rr,
		"Type":       recordType,
		"Value":      value,
		"Format":     "JSON",
		"Version":    "2015-01-09",
	}

	_, err := makeAliDNSRequest(params, accessKey, accessSecret)
	return err
}

// updateDNSRecord 更新 DNS 记录
func updateDNSRecord(recordId, recordType, rr, value, accessKey, accessSecret string) error {
	params := map[string]string{
		"Action":   "UpdateDomainRecord",
		"RecordId": recordId,
		"RR":       rr,
		"Type":     recordType,
		"Value":    value,
		"Format":   "JSON",
		"Version":  "2015-01-09",
	}

	_, err := makeAliDNSRequest(params, accessKey, accessSecret)
	return err
}

// makeAliDNSRequest 发起阿里云 DNS API 请求
func makeAliDNSRequest(params map[string]string, accessKey, accessSecret string) ([]byte, error) {
	// 添加公共参数
	params["SignatureMethod"] = "HMAC-SHA1"
	params["SignatureVersion"] = "1.0"
	params["SignatureNonce"] = fmt.Sprintf("%d", time.Now().UnixNano())
	params["Timestamp"] = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	params["AccessKeyId"] = accessKey

	// 构建签名字符串
	signStr := buildSignString(params)

	// 计算签名
	signature := sign(signStr, accessSecret+"&")
	params["Signature"] = signature

	// 构建请求 URL
	reqURL := "https://alidns.aliyuncs.com/?" + buildQueryString(params)

	// 发起请求
	resp, err := http.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("DNS API 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DNS API 请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// buildSignString 构建签名字符串
func buildSignString(params map[string]string) string {
	var keys []string
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(params[k]))
	}

	queryString := strings.Join(parts, "&")
	return "GET&%2F&" + url.QueryEscape(queryString)
}

// buildQueryString 构建查询字符串
func buildQueryString(params map[string]string) string {
	var parts []string
	for k, v := range params {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
	}
	return strings.Join(parts, "&")
}

// sign 计算 HMAC-SHA1 签名
func sign(stringToSign, key string) string {
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// containsAllIPs 检查现有值是否包含所有 IP
func containsAllIPs(existingValue string, ips []string) bool {
	existingIPs := strings.Split(existingValue, ",")
	existingIPMap := make(map[string]bool)

	for _, ip := range existingIPs {
		existingIPMap[strings.TrimSpace(ip)] = true
	}

	for _, ip := range ips {
		if !existingIPMap[ip] {
			return false
		}
	}

	return len(existingIPs) == len(ips)
}

// RemoveDNSRecord 删除DNS记录
func RemoveDNSRecord(service *model.Service, env model.Env, accessKey, accessSecret string) error {
	envConfig := service.TestEnv
	if env == model.Prod {
		envConfig = service.ProdEnv
	}

	if envConfig == nil || envConfig.Domain == "" {
		fmt.Println("跳过 DNS 记录删除 - 未配置域名")
		return nil
	}

	domain := envConfig.Domain
	// 解析主域名和子域名
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return fmt.Errorf("域名格式不正确: %s", domain)
	}

	var recordName string
	var mainDomain string

	if len(parts) == 2 {
		// 如果是 example.com，则 recordName 为 @
		recordName = "@"
		mainDomain = domain
	} else {
		// 如果是 api.example.com，则 recordName 为 api，mainDomain 为 example.com
		recordName = strings.Join(parts[:len(parts)-2], ".")
		mainDomain = strings.Join(parts[len(parts)-2:], ".")
	}

	fmt.Printf("删除 DNS 记录: %s (主域名: %s, 记录名: %s)\n", domain, mainDomain, recordName)

	// 获取现有的 DNS 记录
	existingRecords, err := getDNSRecords(mainDomain, accessKey, accessSecret)
	if err != nil {
		return fmt.Errorf("获取 DNS 记录失败: %w", err)
	}

	// 查找需要删除的记录
	var recordToDelete *DNSRecord
	for _, record := range existingRecords {
		if record.RR == recordName && record.Type == "A" {
			recordToDelete = &record
			break
		}
	}

	if recordToDelete == nil {
		fmt.Println("DNS 记录不存在，无需删除")
		return nil
	}

	// 删除 DNS 记录
	err = deleteDNSRecord(recordToDelete.RecordId, accessKey, accessSecret)
	if err != nil {
		return fmt.Errorf("删除 DNS 记录失败: %w", err)
	}

	fmt.Printf("DNS 记录删除成功: %s\n", domain)
	return nil
}

// deleteDNSRecord 删除 DNS 记录
func deleteDNSRecord(recordId, accessKey, accessSecret string) error {
	params := map[string]string{
		"Action":   "DeleteDomainRecord",
		"RecordId": recordId,
		"Format":   "JSON",
		"Version":  "2015-01-09",
	}

	_, err := makeAliDNSRequest(params, accessKey, accessSecret)
	return err
}
