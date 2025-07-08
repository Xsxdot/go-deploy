package nginx

import (
	"context"
	"deploy/executor"
	log "deploy/logger"
	"deploy/model"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var logger = log.Log

// CheckAndCreateNginxConfig 检查并创建nginx配置文件
func CheckAndCreateNginxConfig(service *model.Service, env model.Env, selectedServers []string, nginxServers []string, nginxConfDir string) error {
	fmt.Println("检查nginx配置文件...")

	var envConfig *model.EnvConfig
	if env == model.Test {
		envConfig = service.TestEnv
	} else {
		envConfig = service.ProdEnv
	}

	if envConfig == nil {
		return fmt.Errorf("未找到环境配置")
	}

	// 构建配置文件名
	configFileName := fmt.Sprintf("%s_%s.conf", service.ServiceName, string(env))
	remoteConfigPath := filepath.Join(nginxConfDir, configFileName)

	// 创建executor实例
	exec := executor.NewExecutor()
	ctx := context.Background()

	needCreate := make([]string, 0)

	// 先检查所有nginx服务器上是否已存在配置文件
	for _, nginxServer := range nginxServers {
		exists, err := checkConfigFileExists(ctx, exec, nginxServer, remoteConfigPath)
		if err != nil {
			return fmt.Errorf("检查nginx服务器 %s 上的配置文件失败: %w", nginxServer, err)
		}

		if exists {
			fmt.Printf("配置文件已存在于nginx服务器 %s:%s，跳过创建\n", nginxServer, remoteConfigPath)
		} else {
			needCreate = append(needCreate, nginxServer)
		}
	}

	if len(needCreate) == 0 {
		fmt.Printf("所有nginx服务器已存在配置文件，跳过创建\n")
		return nil
	}

	return CreateNginxConf(service, env, selectedServers, needCreate, nginxConfDir)
}

func CreateNginxConf(service *model.Service, env model.Env, selectedServers []string, needCreate []string, nginxConfDir string) error {
	envConfig := service.TestEnv
	if env == model.Prod {
		envConfig = service.ProdEnv
	}
	if envConfig == nil {
		return fmt.Errorf("没有找到对应环境配置")
	}

	// 构建配置文件名
	configFileName := fmt.Sprintf("%s_%s.conf", service.ServiceName, string(env))
	remoteConfigPath := filepath.Join(nginxConfDir, configFileName)

	// 创建executor实例
	exec := executor.NewExecutor()
	ctx := context.Background()

	// 判断应用类型并创建相应配置
	hasBackend := service.BackendWorkDir != ""
	var configContent string

	if hasBackend {
		// 有后端应用，创建反向代理配置（后端会自动代理前端）
		configContent = createReverseProxyConfig(service, env, envConfig, selectedServers)
	} else {
		// 纯前端应用，创建静态文件配置
		configContent = createStaticFileConfig(service, envConfig)
	}

	// 先在本地创建配置文件
	localConfigPath := fmt.Sprintf("/tmp/%s", configFileName)
	err := os.WriteFile(localConfigPath, []byte(configContent), 0644)
	if err != nil {
		return fmt.Errorf("创建本地nginx配置文件失败: %w", err)
	}

	fmt.Printf("创建本地nginx配置文件: %s\n", localConfigPath)

	// 传输配置文件到所有nginx服务器
	for _, nginxServer := range needCreate {
		err = uploadConfigToNginxServer(ctx, exec, nginxServer, localConfigPath, remoteConfigPath)
		if err != nil {
			return fmt.Errorf("传输配置文件到nginx服务器 %s 失败: %w", nginxServer, err)
		}
		fmt.Printf("配置文件已传输到nginx服务器: %s:%s\n", nginxServer, remoteConfigPath)
	}

	// 清理本地临时文件
	os.Remove(localConfigPath)

	return nil
}

// checkConfigFileExists 检查配置文件是否存在
func checkConfigFileExists(ctx context.Context, exec *executor.Executor, nginxServer, remotePath string) (bool, error) {
	// 使用test命令检查文件是否存在
	checkCmd := &executor.Command{
		ID:      "check-config-file",
		Name:    "检查配置文件是否存在",
		Command: fmt.Sprintf("test -f %s", remotePath),
		Timeout: 30 * 1000000000, // 30秒
	}

	req := &executor.ExecuteRequest{
		Type:       executor.CommandTypeSingle,
		ServerHost: nginxServer,
		Command:    checkCmd,
	}

	result, err := exec.Execute(ctx, req)
	if err != nil {
		return false, fmt.Errorf("执行检查命令失败: %w", err)
	}

	// 如果命令执行成功（退出码为0），说明文件存在
	return result.CommandResult.ExitCode == 0, nil
}

// uploadConfigToNginxServer 上传配置文件到nginx服务器
func uploadConfigToNginxServer(ctx context.Context, exec *executor.Executor, nginxServer, localPath, remotePath string) error {
	// 首先读取本地配置文件内容
	content, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("读取本地配置文件失败: %w", err)
	}

	// 使用cat命令创建远程文件
	createCmd := &executor.Command{
		ID:      "create-nginx-config",
		Name:    "创建nginx配置文件",
		Command: fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", remotePath, string(content)),
		Timeout: 60 * 1000000000, // 60秒
	}

	req := &executor.ExecuteRequest{
		Type:       executor.CommandTypeSingle,
		ServerHost: nginxServer,
		Command:    createCmd,
	}

	result, err := exec.Execute(ctx, req)
	if err != nil {
		return fmt.Errorf("执行上传命令失败: %w", err)
	}

	if result.CommandResult.ExitCode != 0 {
		return fmt.Errorf("上传配置文件失败: %s", result.CommandResult.Stderr)
	}

	// 重新加载nginx配置
	logger.Info("🔄 重新加载%s的Nginx配置...", nginxServer)
	err = ReloadNginxConfig(nginxServer)
	if err != nil {
		logger.Warning("nginx配置重载失败: %v", err)
	} else {
		logger.Success("nginx配置重载成功")
	}

	return nil
}

// createStaticFileConfig 创建静态文件服务配置
func createStaticFileConfig(service *model.Service, envConfig *model.EnvConfig) string {
	var config strings.Builder

	config.WriteString("# 静态文件服务配置\n")
	config.WriteString("server {\n")
	config.WriteString("    listen 80;\n")

	if service.SSL {
		config.WriteString("    listen 443 ssl http2;\n")
		config.WriteString(fmt.Sprintf("    ssl_certificate %s;\n", service.SSLCertPath))
		config.WriteString(fmt.Sprintf("    ssl_certificate_key %s;\n", service.SSLKeyPath))
		config.WriteString("    ssl_protocols TLSv1.2 TLSv1.3;\n")
		config.WriteString("    ssl_ciphers ECDHE-RSA-AES128-GCM-SHA256:ECDHE:ECDH:AES:HIGH:!NULL:!aNULL:!MD5:!ADH:!RC4;\n")
		config.WriteString("    ssl_prefer_server_ciphers on;\n")
		config.WriteString("\n")
	}

	config.WriteString(fmt.Sprintf("    server_name %s;\n", envConfig.Domain))
	config.WriteString("\n")

	// 静态文件根目录
	webRoot := envConfig.InstallPath

	config.WriteString(fmt.Sprintf("    root %s;\n", webRoot))
	config.WriteString("    index index.html index.htm;\n")
	config.WriteString("\n")

	// 静态资源缓存配置
	config.WriteString("    # 静态资源缓存配置\n")
	config.WriteString("    location ~* \\.(css|js|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {\n")
	config.WriteString("        expires 1y;\n")
	config.WriteString("        add_header Cache-Control \"public, immutable\";\n")
	config.WriteString("    }\n")
	config.WriteString("\n")

	// SPA路由配置
	config.WriteString("    # SPA路由配置\n")
	config.WriteString("    location / {\n")
	config.WriteString("        try_files $uri $uri/ /index.html;\n")
	config.WriteString("    }\n")
	config.WriteString("\n")

	// 健康检查
	config.WriteString("    # 健康检查\n")
	config.WriteString("    location /health {\n")
	config.WriteString("        access_log off;\n")
	config.WriteString("        return 200 'healthy';\n")
	config.WriteString("        add_header Content-Type text/plain;\n")
	config.WriteString("    }\n")

	// HTTP重定向到HTTPS
	if service.SSL {
		config.WriteString("\n")
		config.WriteString("    # HTTP重定向到HTTPS\n")
		config.WriteString("    if ($scheme != \"https\") {\n")
		config.WriteString("        return 301 https://$host$request_uri;\n")
		config.WriteString("    }\n")
	}

	config.WriteString("}\n")

	return config.String()
}

// createReverseProxyConfig 创建反向代理配置（后端应用会自动代理前端）
func createReverseProxyConfig(service *model.Service, env model.Env, envConfig *model.EnvConfig, selectedServers []string) string {
	var config strings.Builder

	// 上游服务器配置
	upstreamName := fmt.Sprintf("%s_%s_backend", service.ServiceName, string(env))
	config.WriteString(fmt.Sprintf("# %s 后端服务器池\n", service.ServiceName))
	config.WriteString(fmt.Sprintf("upstream %s {\n", upstreamName))

	for _, server := range selectedServers {
		config.WriteString(fmt.Sprintf("    server %s:%d;\n", server, envConfig.Port))
	}

	config.WriteString("    keepalive 32;\n")
	config.WriteString("}\n")
	config.WriteString("\n")

	// 主服务器配置
	config.WriteString("# 反向代理配置 - 后端应用自动代理前端\n")
	config.WriteString("server {\n")
	config.WriteString("    listen 80;\n")

	if service.SSL {
		config.WriteString("    listen 443 ssl http2;\n")
		config.WriteString(fmt.Sprintf("    ssl_certificate %s;\n", service.SSLCertPath))
		config.WriteString(fmt.Sprintf("    ssl_certificate_key %s;\n", service.SSLKeyPath))
		config.WriteString("    ssl_protocols TLSv1.2 TLSv1.3;\n")
		config.WriteString("    ssl_ciphers ECDHE-RSA-AES128-GCM-SHA256:ECDHE:ECDH:AES:HIGH:!NULL:!aNULL:!MD5:!ADH:!RC4;\n")
		config.WriteString("    ssl_prefer_server_ciphers on;\n")
		config.WriteString("\n")
	}

	config.WriteString(fmt.Sprintf("    server_name %s;\n", envConfig.Domain))
	config.WriteString("\n")

	// 日志配置
	config.WriteString("    # 访问日志\n")
	config.WriteString(fmt.Sprintf("    access_log /var/log/nginx/%s_access.log;\n", service.ServiceName))
	config.WriteString(fmt.Sprintf("    error_log /var/log/nginx/%s_error.log;\n", service.ServiceName))
	config.WriteString("\n")

	// 所有请求都代理到后端（后端会自动处理前端静态资源）
	config.WriteString("    # 所有请求代理到后端服务器池\n")
	config.WriteString("    location / {\n")
	config.WriteString(fmt.Sprintf("        proxy_pass http://%s;\n", upstreamName))
	config.WriteString("        proxy_set_header Host $host;\n")
	config.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
	config.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	config.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	config.WriteString("\n")
	config.WriteString("        # 代理超时设置\n")
	config.WriteString("        proxy_connect_timeout 30s;\n")
	config.WriteString("        proxy_send_timeout 30s;\n")
	config.WriteString("        proxy_read_timeout 30s;\n")
	config.WriteString("\n")
	config.WriteString("        # 保持连接\n")
	config.WriteString("        proxy_http_version 1.1;\n")
	config.WriteString("        proxy_set_header Connection \"\";\n")
	config.WriteString("    }\n")

	// HTTP重定向到HTTPS
	if service.SSL {
		config.WriteString("\n")
		config.WriteString("    # HTTP重定向到HTTPS\n")
		config.WriteString("    if ($scheme != \"https\") {\n")
		config.WriteString("        return 301 https://$host$request_uri;\n")
		config.WriteString("    }\n")
	}

	config.WriteString("}\n")

	return config.String()
}

// ReloadNginxConfig 重新加载nginx配置
func ReloadNginxConfig(server string) error {
	fmt.Println("重新加载nginx配置...")

	// 创建executor实例
	exec := executor.NewExecutor()
	ctx := context.Background()

	fmt.Printf("在nginx服务器 %s 上重新加载配置\n", server)

	// 先测试配置文件语法
	testCmd := &executor.Command{
		ID:      "nginx-test-config",
		Name:    "nginx配置语法检查",
		Command: "nginx -t",
		Timeout: 30 * 1000000000, // 30秒
	}

	testReq := &executor.ExecuteRequest{
		Type:       executor.CommandTypeSingle,
		ServerHost: server,
		Command:    testCmd,
	}

	testResult, err := exec.Execute(ctx, testReq)
	if err != nil {
		return fmt.Errorf("执行nginx配置语法检查失败在服务器 %s: %w", server, err)
	}

	if testResult.CommandResult.ExitCode != 0 {
		return fmt.Errorf("nginx配置语法检查失败在服务器 %s: %s", server, testResult.CommandResult.Stderr)
	}

	// 重新加载nginx配置
	reloadCmd := &executor.Command{
		ID:      "nginx-reload",
		Name:    "重新加载nginx配置",
		Command: "nginx -s reload",
		Timeout: 30 * 1000000000, // 30秒
	}

	reloadReq := &executor.ExecuteRequest{
		Type:       executor.CommandTypeSingle,
		ServerHost: server,
		Command:    reloadCmd,
	}

	reloadResult, err := exec.Execute(ctx, reloadReq)
	if err != nil {
		return fmt.Errorf("执行nginx重新加载失败在服务器 %s: %w", server, err)
	}

	if reloadResult.CommandResult.ExitCode != 0 {
		return fmt.Errorf("nginx重新加载失败在服务器 %s: %s", server, reloadResult.CommandResult.Stderr)
	}

	fmt.Printf("nginx配置重新加载成功: %s\n", server)

	return nil
}

// RemoveNginxConfig 删除nginx配置文件
func RemoveNginxConfig(service *model.Service, env model.Env, nginxServers []string, nginxConfDir string) error {
	fmt.Println("删除nginx配置文件...")

	// 构建配置文件名
	configFileName := fmt.Sprintf("%s_%s.conf", service.ServiceName, string(env))
	remoteConfigPath := filepath.Join(nginxConfDir, configFileName)

	// 创建executor实例
	exec := executor.NewExecutor()
	ctx := context.Background()

	// 从所有nginx服务器删除配置文件
	for _, nginxServer := range nginxServers {
		err := removeConfigFromNginxServer(ctx, exec, nginxServer, remoteConfigPath)
		if err != nil {
			logger.Error("从nginx服务器 %s 删除配置文件失败: %w", nginxServer, err)
		}
		fmt.Printf("配置文件已从nginx服务器删除: %s:%s\n", nginxServer, remoteConfigPath)
		// 重新加载nginx配置
		err = ReloadNginxConfig(nginxServer)
		if err != nil {
			logger.Error("nginx配置重载失败: %w", err)
		}
	}

	fmt.Println("nginx配置删除完成")
	return nil
}

// removeConfigFromNginxServer 从nginx服务器删除配置文件
func removeConfigFromNginxServer(ctx context.Context, exec *executor.Executor, nginxServer, remotePath string) error {
	// 使用rm命令删除文件
	removeCmd := &executor.Command{
		ID:      "remove-nginx-config",
		Name:    "删除nginx配置文件",
		Command: fmt.Sprintf("rm -f %s", remotePath),
		Timeout: 30 * 1000000000, // 30秒
	}

	req := &executor.ExecuteRequest{
		Type:       executor.CommandTypeSingle,
		ServerHost: nginxServer,
		Command:    removeCmd,
	}

	result, err := exec.Execute(ctx, req)
	if err != nil {
		return fmt.Errorf("执行删除命令失败: %w", err)
	}

	if result.CommandResult.ExitCode != 0 {
		return fmt.Errorf("删除配置文件失败: %s", result.CommandResult.Stderr)
	}

	return nil
}
