package systemd

import (
	"context"
	"deploy/executor"
	"fmt"
	"strings"
	"time"
)

// ServiceConfig systemd服务配置
type ServiceConfig struct {
	ServiceName string
	Description string
	ExecStart   string
	WorkingDir  string
	User        string
	Group       string
	Restart     string
	RestartSec  string
	Environment []string
}

// CreateSystemdService 创建systemd service文件
func CreateSystemdService(exec *executor.Executor, host string, config ServiceConfig) error {
	ctx := context.Background()

	serviceFilePath := fmt.Sprintf("/etc/systemd/system/%s.service", config.ServiceName)

	// 检查service文件是否已存在
	checkCmd := fmt.Sprintf("test -f %s", serviceFilePath)
	resp, err := exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: checkCmd,
			Timeout: 10 * time.Second,
		},
	})

	// 如果命令执行成功且退出码为0，说明文件已存在，则跳过创建
	if err == nil && resp != nil && resp.CommandResult != nil && resp.CommandResult.ExitCode == 0 {
		fmt.Printf("systemd service文件已存在: %s\n", serviceFilePath)
		return nil
	}

	// 生成service文件内容
	serviceContent := generateServiceContent(config)

	// 创建临时文件并写入内容
	tempFile := fmt.Sprintf("/tmp/%s.service", config.ServiceName)
	createFileCmd := fmt.Sprintf("cat > \"%s\" << 'EOF'\n%s\nEOF", tempFile, serviceContent)

	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: createFileCmd,
			Timeout: 30 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("创建临时service文件失败: %w", err)
	}

	// 移动文件到systemd目录
	moveCmd := fmt.Sprintf("mv %s %s", tempFile, serviceFilePath)
	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: moveCmd,
			Timeout: 10 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("移动service文件失败: %w", err)
	}

	// 重新加载systemd配置
	reloadCmd := "systemctl daemon-reload"
	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: reloadCmd,
			Timeout: 10 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("重新加载systemd配置失败: %w", err)
	}

	// 启用服务
	enableCmd := fmt.Sprintf("systemctl enable %s", config.ServiceName)
	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: enableCmd,
			Timeout: 10 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("启用systemd服务失败: %w", err)
	}

	fmt.Printf("systemd service文件创建成功: %s\n", serviceFilePath)
	return nil
}

// generateServiceContent 生成systemd service文件内容
func generateServiceContent(config ServiceConfig) string {
	// 设置默认值
	if config.Description == "" {
		config.Description = fmt.Sprintf("%s service", config.ServiceName)
	}
	if config.User == "" {
		config.User = "root"
	}
	if config.Group == "" {
		config.Group = "root"
	}
	if config.Restart == "" {
		config.Restart = "always"
	}
	if config.RestartSec == "" {
		config.RestartSec = "10"
	}

	var serviceContent strings.Builder
	serviceContent.WriteString("[Unit]\n")
	serviceContent.WriteString(fmt.Sprintf("Description=%s\n", config.Description))
	serviceContent.WriteString("After=network.target\n")
	serviceContent.WriteString("\n")

	serviceContent.WriteString("[Service]\n")
	serviceContent.WriteString("Type=simple\n")
	serviceContent.WriteString(fmt.Sprintf("User=%s\n", config.User))
	serviceContent.WriteString(fmt.Sprintf("Group=%s\n", config.Group))

	if config.WorkingDir != "" {
		serviceContent.WriteString(fmt.Sprintf("WorkingDirectory=%s\n", config.WorkingDir))
	}

	serviceContent.WriteString(fmt.Sprintf("ExecStart=%s\n", config.ExecStart))
	serviceContent.WriteString(fmt.Sprintf("Restart=%s\n", config.Restart))
	serviceContent.WriteString(fmt.Sprintf("RestartSec=%s\n", config.RestartSec))

	// 添加环境变量
	for _, env := range config.Environment {
		serviceContent.WriteString(fmt.Sprintf("Environment=%s\n", env))
	}

	serviceContent.WriteString("\n")
	serviceContent.WriteString("[Install]\n")
	serviceContent.WriteString("WantedBy=multi-user.target\n")

	return serviceContent.String()
}

// UninstallSystemdService 卸载systemd服务
func UninstallSystemdService(exec *executor.Executor, serviceName, host string) error {
	ctx := context.Background()

	serviceFilePath := fmt.Sprintf("/etc/systemd/system/%s.service", serviceName)

	fmt.Printf("卸载服务器 %s 上的systemd服务: %s\n", host, serviceName)

	// 停止服务
	stopCmd := fmt.Sprintf("systemctl stop %s", serviceName)
	_, err := exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: stopCmd,
			Timeout: 30 * time.Second,
		},
	})
	if err != nil {
		fmt.Printf("警告: 停止服务失败: %v\n", err)
	}

	// 禁用服务
	disableCmd := fmt.Sprintf("systemctl disable %s", serviceName)
	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: disableCmd,
			Timeout: 30 * time.Second,
		},
	})
	if err != nil {
		fmt.Printf("警告: 禁用服务失败: %v\n", err)
	}

	// 删除service文件
	removeCmd := fmt.Sprintf("rm -f %s", serviceFilePath)
	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: removeCmd,
			Timeout: 10 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("删除service文件失败: %w", err)
	}

	// 重新加载systemd配置
	reloadCmd := "systemctl daemon-reload"
	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: reloadCmd,
			Timeout: 10 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("重新加载systemd配置失败: %w", err)
	}

	fmt.Printf("服务器 %s 上的systemd服务卸载成功: %s\n", host, serviceName)
	return nil
}
