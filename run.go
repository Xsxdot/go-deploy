package main

import (
	"bufio"
	"context"
	"deploy/dns"
	"deploy/executor"
	log "deploy/logger"
	"deploy/model"
	"deploy/nginx"
	"deploy/systemd"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
)

var logger = log.Log

// executeCommand 执行本地命令并实时打印输出
func executeCommand(workDir, command string) error {
	logger.Command("%s", command)

	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = workDir

	// 创建管道来捕获输出
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建stdout管道失败: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("创建stderr管道失败: %w", err)
	}

	// 启动命令
	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("启动命令失败: %w", err)
	}

	// 创建goroutine来实时读取输出
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Printf("    %s%s│%s %s\n", log.ColorGreen, log.ColorBold, log.ColorReset, line)
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Printf("    %s%s│%s %s%s%s\n", log.ColorRed, log.ColorBold, log.ColorReset, log.ColorRed, line, log.ColorReset)
		}
	}()

	// 等待命令完成
	err = cmd.Wait()
	if err != nil {
		return fmt.Errorf("命令执行失败: %w", err)
	}

	return nil
}

// executeCommandWithProgress 执行带进度提示的命令
func executeCommandWithProgress(workDir, command, progressMsg string) error {
	logger.Progress("%s", progressMsg)
	return executeCommand(workDir, command)
}

// copyWithProgress 带进度的文件复制
func copyWithProgress(src, dst string) error {
	logger.Progress("复制文件: %s -> %s", src, dst)

	// 确保目标目录存在
	targetDir := filepath.Dir(dst)
	err := os.MkdirAll(targetDir, 0755)
	if err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	return executeCommand("", fmt.Sprintf("cp -r '%s' '%s'", src, dst))
}

// moveWithProgress 带进度的文件移动
func moveWithProgress(src, dst string) error {
	logger.Progress("移动文件: %s -> %s", src, dst)

	// 确保目标目录存在
	targetDir := filepath.Dir(dst)
	err := os.MkdirAll(targetDir, 0755)
	if err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	// 检查源路径是否以 /* 结尾，如果是则需要特殊处理
	if strings.HasSuffix(src, "/*") {
		// 移动文件夹内的所有内容，而不是文件夹本身
		srcDir := strings.TrimSuffix(src, "/*")
		// 使用 bash 的 shopt 来启用 dotglob，确保隐藏文件也被移动
		// 然后使用 mv 命令移动所有内容
		cmd := fmt.Sprintf("shopt -s dotglob && mv '%s'/* '%s' 2>/dev/null || true", srcDir, dst)
		return executeCommand("", cmd)
	}

	return executeCommand("", fmt.Sprintf("mv '%s' '%s'", src, dst))
}

// createTarWithProgress 带进度的打包
func createTarWithProgress(packagePath, tmpDir string) error {
	logger.Progress("创建压缩包: %s", packagePath)
	return executeCommand("", fmt.Sprintf("tar -czf '%s' -C '%s' .", packagePath, tmpDir))
}

// uploadWithProgress 带进度的文件上传
func uploadWithProgress(host, localPath, remotePath string) error {
	logger.Progress("上传文件到 %s: %s -> %s", host, localPath, remotePath)
	return executeCommand("", fmt.Sprintf("scp '%s' 'root@%s:%s'", localPath, host, remotePath))
}

func Deploy(service *model.Service, env model.Env, version, desc string, buildType string, serverHost []string) error {
	logger.Separator()
	logger.Step(1, 2, "开始部署流程 - 服务: %s, 版本: %s, 环境: %s, 构建类型: %s", service.ServiceName, version, env, buildType)
	logger.Info("目标服务器: %v", serverHost)

	// 第一步：构建阶段
	logger.Step(1, 2, "构建和打包阶段")
	packagePath, err := buildAndPackage(service, env, version, desc, buildType)
	if err != nil {
		logger.Error("构建打包失败: %v", err)
		return fmt.Errorf("构建打包失败: %w", err)
	}
	logger.Success("构建打包完成: %s", packagePath)

	// 第二步：部署阶段
	logger.Step(2, 2, "服务器部署阶段")
	err = deployToServers(service, env, version, buildType, serverHost, packagePath)
	if err != nil {
		logger.Error("服务器部署失败: %v", err)
		return err
	}

	logger.Separator()
	logger.Success("🎉 部署流程全部完成！")
	return nil
}

func Rollback(service *model.Service, env model.Env, version string, buildType string, serverHost []string) error {
	logger.Separator()
	logger.Step(1, 2, "开始回滚流程 - 服务: %s, 版本: %s, 环境: %s, 构建类型: %s", service.ServiceName, version, env, buildType)
	logger.Info("目标服务器: %v", serverHost)

	// 第一步：获取已有构建包
	logger.Step(1, 2, "获取已有构建包")
	packagePath, err := getExistingPackage(service.ServiceName, version, buildType)
	if err != nil {
		logger.Error("获取构建包失败: %v", err)
		return fmt.Errorf("获取构建包失败: %w", err)
	}
	logger.Success("找到构建包: %s", packagePath)

	// 第二步：部署阶段
	logger.Step(2, 2, "执行回滚部署")
	err = deployToServers(service, env, version, buildType, serverHost, packagePath)
	if err != nil {
		logger.Error("回滚部署失败: %v", err)
		return err
	}

	logger.Separator()
	logger.Success("🔄 回滚流程全部完成！")
	return nil
}

// buildAndPackage 构建并打包
func buildAndPackage(service *model.Service, env model.Env, version, desc string, buildType string) (string, error) {
	logger.Info("开始构建阶段...")

	// 创建临时目录
	tmpDir := fmt.Sprintf("/tmp/deploy_%s_%s_%d", service.ServiceName, version, time.Now().Unix())
	logger.Progress("创建临时目录: %s", tmpDir)
	err := os.MkdirAll(tmpDir, 0755)
	if err != nil {
		logger.Error("创建临时目录失败: %v", err)
		return "", fmt.Errorf("创建临时目录失败: %w", err)
	}

	var envConfig *model.EnvConfig
	if env == model.Test {
		envConfig = service.TestEnv
	} else {
		envConfig = service.ProdEnv
	}

	// 执行构建命令
	if buildType == "all" || buildType == "front" {
		if service.FrontendWorkDir != "" && len(envConfig.FrontendBuildCommands) > 0 {
			logger.Info("🔨 执行前端构建...")
			err := executeBuildCommands(service.FrontendWorkDir, envConfig.FrontendBuildCommands)
			if err != nil {
				logger.Error("前端构建失败: %v", err)
				return "", fmt.Errorf("前端构建失败: %w", err)
			}
			logger.Success("前端构建完成")
		}
	}

	if buildType == "all" || buildType == "back" {
		if service.BackendWorkDir != "" && len(envConfig.BackendBuildCommands) > 0 {
			logger.Info("🔨 执行后端构建...")
			err := executeBuildCommands(service.BackendWorkDir, envConfig.BackendBuildCommands)
			if err != nil {
				logger.Error("后端构建失败: %v", err)
				return "", fmt.Errorf("后端构建失败: %w", err)
			}
			logger.Success("后端构建完成")
		}
	}

	// 复制文件到临时目录
	logger.Info("📁 复制文件到临时目录...")
	err = copyFilesToTemp(service, env, tmpDir, buildType)
	if err != nil {
		logger.Error("复制文件失败: %v", err)
		return "", fmt.Errorf("复制文件失败: %w", err)
	}
	logger.Success("文件复制完成")

	// 打包
	logger.Info("📦 创建部署包...")
	packagePath, err := createPackage(env, service.ServiceName, version, desc, buildType, tmpDir)
	if err != nil {
		logger.Error("打包失败: %v", err)
		return "", fmt.Errorf("打包失败: %w", err)
	}

	logger.Success("构建完成，包文件: %s", packagePath)
	return packagePath, nil
}

// getExistingPackage 获取已有的构建包
func getExistingPackage(serviceName, version, buildType string) (string, error) {
	versionsDir := filepath.Join(backupDir, serviceName)

	// 查找匹配的包文件
	files, err := os.ReadDir(versionsDir)
	if err != nil {
		return "", fmt.Errorf("读取版本目录失败: %w", err)
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), version) {
			return filepath.Join(versionsDir, file.Name()), nil
		}
	}

	return "", fmt.Errorf("未找到版本%s的%s构建包", version, buildType)
}

// executeBuildCommands 执行构建命令
func executeBuildCommands(workDir string, commands []string) error {
	for i, command := range commands {
		logger.Progress("执行构建命令 [%d/%d]: %s", i+1, len(commands), command)
		err := executeCommand(workDir, command)
		if err != nil {
			logger.Error("命令执行失败: %s", command)
			return fmt.Errorf("命令执行失败: %s, 错误: %w", command, err)
		}
		logger.Success("命令执行成功: %s", command)
	}
	return nil
}

// copyFilesToTemp 复制文件到临时目录
func copyFilesToTemp(service *model.Service, env model.Env, tmpDir, buildType string) error {
	for _, copyFile := range service.CopyFiles {
		// 检查是否需要复制该文件
		if buildType == "front" && copyFile.Type != "front" {
			continue
		}
		if buildType == "back" && copyFile.Type != "back" {
			continue
		}

		// 替换变量
		source := strings.ReplaceAll(copyFile.Source, "${workDir}", tmpDir)
		source = strings.ReplaceAll(copyFile.Source, "${env}", string(env))
		target := strings.ReplaceAll(copyFile.Target, "${workDir}", tmpDir)
		target = strings.ReplaceAll(target, "${env}", string(env))

		switch copyFile.Mode {
		case model.ModeMove:
			err := moveWithProgress(source, target)
			if err != nil {
				logger.Error("移动文件失败 %s -> %s: %v", source, target, err)
				return fmt.Errorf("移动文件失败 %s -> %s: %w", source, target, err)
			}
		case model.ModeCopy:
			err := copyWithProgress(source, target)
			if err != nil {
				logger.Error("复制文件失败 %s -> %s: %v", source, target, err)
				return fmt.Errorf("复制文件失败 %s -> %s: %w", source, target, err)
			}
		case model.ModeMkdir:
			logger.Progress("创建目录: %s", target)
			err := os.MkdirAll(target, 0755)
			if err != nil {
				logger.Error("创建目录失败 %s: %v", target, err)
				return fmt.Errorf("创建目录失败 %s: %w", target, err)
			}
		}
	}
	return nil
}

// createPackage 创建压缩包
func createPackage(env model.Env, serviceName, version, desc, buildType, tmpDir string) (string, error) {
	packageName := fmt.Sprintf("%s-%s-%s-%s.tar.gz", version, string(env), buildType, desc)

	// 确保版本目录存在
	versionsDir := filepath.Join(backupDir, serviceName)
	logger.Progress("创建版本目录: %s", versionsDir)
	err := os.MkdirAll(versionsDir, 0755)
	if err != nil {
		logger.Error("创建版本目录失败: %v", err)
		return "", fmt.Errorf("创建版本目录失败: %w", err)
	}

	packagePath := filepath.Join(versionsDir, packageName)

	// 使用 tar 命令创建压缩包
	err = createTarWithProgress(packagePath, tmpDir)
	if err != nil {
		logger.Error("创建压缩包失败: %v", err)
		return "", fmt.Errorf("创建压缩包失败: %w", err)
	}

	return packagePath, nil
}

// deployToServers 部署到服务器
func deployToServers(service *model.Service, env model.Env, version, buildType string, serverHost []string, packagePath string) error {
	logger.Info("开始部署到服务器...")

	executor := executor.NewExecutor()

	for i, host := range serverHost {
		logger.Separator()
		logger.Step(i+1, len(serverHost), "部署到服务器: %s", host)

		// 第一阶段：核心部署
		logger.Info("🚀 执行核心部署...")
		deployResult, err := deployToSingleServer(executor, service, env, version, buildType, host, packagePath)
		if err != nil {
			logger.Error("服务器 %s 部署失败: %v", host, err)

			// 询问用户是否继续
			var continueChoice string
			prompt := &survey.Select{
				Message: fmt.Sprintf("服务器 %s 部署失败，请选择操作:", host),
				Options: []string{"重试部署", "继续下一台", "停止部署"},
				Default: "重试部署",
			}
			survey.AskOne(prompt, &continueChoice)

			switch continueChoice {
			case "重试部署":
				logger.Info("🔄 重试部署...")
				// 重试当前服务器部署
				deployResult, err = deployToSingleServer(executor, service, env, version, buildType, host, packagePath)
				if err != nil {
					logger.Error("重试部署失败: %v", err)
					return err
				}
			case "继续下一台":
				logger.Warning("跳过当前服务器，继续下一台")
				continue
			case "停止部署":
				logger.Warning("用户选择停止部署")
				return fmt.Errorf("用户选择停止部署")
			}
		}
		logger.Success("核心部署完成")

		// 第二阶段：健康检查
		if buildType == "front" {
			// 前端部署：简化健康检查，只验证文件部署是否成功
			logger.Info("🎨 前端部署健康检查：验证文件部署状态...")
			err = performFrontendHealthCheck(executor, service, env, host)
			if err != nil {
				logger.Warning("前端文件部署验证失败: %v", err)
				// 前端部署失败通常不需要回滚，因为没有停止服务
				var choice string
				prompt := &survey.Select{
					Message: fmt.Sprintf("服务器 %s 前端文件验证失败，请选择操作:", host),
					Options: []string{"继续", "退出"},
					Default: "继续",
				}
				survey.AskOne(prompt, &choice)

				if choice == "退出" {
					return fmt.Errorf("前端文件验证失败")
				}
				logger.Warning("跳过前端文件验证，继续部署")
			} else {
				logger.Success("前端文件部署验证通过")
			}
		} else {
			// 全量/后端部署：执行完整的健康检查
			logger.Info("🏥 执行后端服务健康检查...")
			for {
				err = performServerHealthCheck(service, env, host)
				if err != nil {
					logger.Error("服务器 %s 健康检查失败: %v", host, err)

					// 询问用户操作
					var healthChoice string
					prompt := &survey.Select{
						Message: fmt.Sprintf("服务器 %s 健康检查失败，请选择操作:", host),
						Options: []string{"重试健康检查", "继续", "回滚", "退出"},
						Default: "重试健康检查",
					}
					survey.AskOne(prompt, &healthChoice)

					switch healthChoice {
					case "重试健康检查":
						logger.Info("🔄 重试健康检查...")
						continue // 继续健康检查循环
					case "继续":
						logger.Warning("跳过健康检查，继续部署")
						break // 跳出健康检查循环
					case "回滚":
						logger.Warning("开始回滚操作...")
						// 回滚操作
						rollbackErr := rollbackSingleServer(executor, service, env, host, deployResult.BackupPath)
						if rollbackErr != nil {
							logger.Error("回滚失败: %v", rollbackErr)
						} else {
							logger.Success("回滚成功")
						}
						return fmt.Errorf("健康检查失败，已回滚")
					case "退出":
						return fmt.Errorf("健康检查失败")
					}
				} else {
					logger.Success("健康检查通过")
				}
				break // 健康检查成功，跳出循环
			}
		}

		// 第三阶段：清理文件
		logger.Info("🧹 清理临时文件...")
		err = cleanupServerFiles(executor, host, deployResult)
		if err != nil {
			logger.Warning("服务器 %s 清理临时文件失败: %v", host, err)
		} else {
			logger.Success("临时文件清理完成")
		}

		logger.Success("✅ 服务器 %s 部署成功", host)
	}

	logger.Separator()
	logger.Success("🎉 所有服务器部署完成")

	// 第四阶段：检查并创建nginx配置文件（仅在非纯前端部署时）
	if buildType != "front" || service.BackendWorkDir == "" {
		logger.Info("🔧 检查nginx配置...")
		err := nginx.CheckAndCreateNginxConfig(service, env, serverHost, cfg.NginxServers, cfg.NginxConfDir)
		if err != nil {
			logger.Warning("nginx配置处理失败: %v", err)
		} else {
			logger.Success("nginx配置创建成功")
		}

		// 第五阶段：检查并配置DNS记录
		logger.Info("🌐 检查DNS配置...")
		err = dns.CheckAndCreateDNSRecord(service, env, cfg.NginxServers, cfg.DNSKey, cfg.DNSSecret)
		if err != nil {
			logger.Warning("DNS配置处理失败: %v", err)
		} else {
			logger.Success("DNS配置创建成功")
		}
	} else {
		logger.Info("🎨 纯前端部署，跳过nginx和DNS配置")
	}

	return nil
}

// DeployResult 部署结果
type DeployResult struct {
	TempPath   string
	BackupPath string
}

// deployToSingleServer 部署到单个服务器（仅处理核心部署逻辑）
func deployToSingleServer(exec *executor.Executor, service *model.Service, env model.Env, version, buildType, host, packagePath string) (*DeployResult, error) {
	ctx := context.Background()

	envCfg := service.TestEnv
	if env == model.Prod {
		envCfg = service.ProdEnv
	}

	if envCfg == nil {
		return nil, errors.New("envConfig is nil")
	}
	// 1. 上传包文件到服务器临时目录
	tempPath := fmt.Sprintf("/tmp/%s", filepath.Base(packagePath))
	logger.Info("📤 上传部署包到服务器...")
	err := uploadWithProgress(host, packagePath, tempPath)
	if err != nil {
		logger.Error("上传文件失败: %v", err)
		return nil, fmt.Errorf("上传文件失败: %w", err)
	}
	logger.Success("部署包上传完成")

	// 2. 检查并创建systemd service文件（仅在非前端应用且启用systemd时）
	if buildType != "front" {
		err = createSystemdServiceFile(exec, service, host, env)
		if err != nil {
			return nil, fmt.Errorf("创建systemd service文件失败: %w", err)
		}
	}

	// 3. 备份现有安装目录和部署文件
	backupPath := fmt.Sprintf("/tmp/%s_backup_%d", envCfg.InstallPath, time.Now().Unix())

	if buildType == "front" {
		// 纯前端部署：只备份和更新前端文件，不停止后端服务
		logger.Info("🎨 纯前端部署模式：仅更新前端文件，不影响后端服务")

		// 创建备份目录（仅备份前端文件）
		_, err = exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("mkdir -p '%s'", backupPath),
				Timeout: 30 * time.Second,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("创建备份目录失败: %w", err)
		}

		// 直接解压到安装目录（覆盖前端文件）
		_, err = exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("mkdir -p '%s' && cd '%s' && tar -xzf '%s'", envCfg.InstallPath, envCfg.InstallPath, tempPath),
				Timeout: 120 * time.Second,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("解压前端文件失败: %w", err)
		}

		logger.Success("前端文件更新完成，后端服务未受影响")
	} else {
		// 全量部署或后端部署：需要停止服务
		logger.Info("🔄 全量/后端部署模式：需要停止服务进行更新")

		// 停止服务
		_, err = exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("systemctl stop %s", service.ServiceName),
				Timeout: 30 * time.Second,
			},
		})
		if err != nil {
			logger.Warning("停止服务失败: %v", err)
		}

		// 备份现有目录
		_, err = exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("if [ -d '%s' ]; then mv '%s' '%s'; fi", envCfg.InstallPath, envCfg.InstallPath, backupPath),
				Timeout: 60 * time.Second,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("备份现有目录失败: %w", err)
		}

		// 创建安装目录并解压
		_, err = exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("mkdir -p '%s' && cd '%s' && tar -xzf '%s'", envCfg.InstallPath, envCfg.InstallPath, tempPath),
				Timeout: 120 * time.Second,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("解压文件失败: %w", err)
		}

		// 启动服务
		_, err = exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("systemctl start %s", service.ServiceName),
				Timeout: 30 * time.Second,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("启动服务失败: %w", err)
		}
	}

	return &DeployResult{
		TempPath:   tempPath,
		BackupPath: backupPath,
	}, nil
}

// performServerHealthCheck 执行服务器健康检查
func performServerHealthCheck(service *model.Service, env model.Env, host string) error {
	envCfg := service.TestEnv
	if env == model.Prod {
		envCfg = service.ProdEnv
	}

	if envCfg == nil {
		return nil
	}

	if envCfg.HealthUrl == "" {
		return nil
	}

	healthUrl := strings.ReplaceAll(envCfg.HealthUrl, "${host}", host)
	return performHealthCheck(healthUrl)
}

// performFrontendHealthCheck 执行前端部署验证检查
func performFrontendHealthCheck(exec *executor.Executor, service *model.Service, env model.Env, host string) error {
	ctx := context.Background()

	envCfg := service.TestEnv
	if env == model.Prod {
		envCfg = service.ProdEnv
	}

	if envCfg == nil {
		return errors.New("envConfig is nil")
	}

	logger.Progress("验证前端文件部署状态...")

	// 检查安装目录是否存在
	result, err := exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: fmt.Sprintf("test -d '%s' && echo 'directory exists'", envCfg.InstallPath),
			Timeout: 10 * time.Second,
		},
	})
	if err != nil || result.CommandResult == nil || !strings.Contains(result.CommandResult.Stdout, "directory exists") {
		return fmt.Errorf("安装目录不存在或无法访问: %s", envCfg.InstallPath)
	}

	// 检查是否有文件在安装目录中
	result, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: fmt.Sprintf("find '%s' -type f -name '*' | head -5", envCfg.InstallPath),
			Timeout: 10 * time.Second,
		},
	})
	if err != nil || result.CommandResult == nil || strings.TrimSpace(result.CommandResult.Stdout) == "" {
		return fmt.Errorf("安装目录中未找到部署文件: %s", envCfg.InstallPath)
	}

	logger.Success("前端文件验证通过：目录存在且包含文件")
	return nil
}

// cleanupServerFiles 清理服务器临时文件
func cleanupServerFiles(exec *executor.Executor, host string, result *DeployResult) error {
	if result == nil {
		return nil
	}

	ctx := context.Background()
	_, err := exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: fmt.Sprintf("rm -f '%s' && rm -rf '%s'", result.TempPath, result.BackupPath),
			Timeout: 30 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("清理临时文件失败: %w", err)
	}
	return nil
}

// performHealthCheck 执行健康检查
func performHealthCheck(url string) error {
	maxRetries := 30
	retryInterval := 2 * time.Second

	logger.Info("开始健康检查: %s", url)

	for i := 0; i < maxRetries; i++ {
		logger.Progress("健康检查 (%d/%d): %s", i+1, maxRetries, url)

		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			logger.Success("健康检查通过 ✅")
			return nil
		}

		if resp != nil {
			if resp.StatusCode != 200 {
				logger.Warning("健康检查失败，状态码: %d", resp.StatusCode)
			}
			resp.Body.Close()
		} else {
			logger.Warning("健康检查失败，错误: %v", err)
		}

		if i < maxRetries-1 {
			logger.Progress("健康检查失败，%v后重试...", retryInterval)
			time.Sleep(retryInterval)
		}
	}

	logger.Error("健康检查失败，已重试%d次", maxRetries)
	return fmt.Errorf("健康检查失败，已重试%d次", maxRetries)
}

// rollbackSingleServer 回滚单个服务器
func rollbackSingleServer(exec *executor.Executor, service *model.Service, env model.Env, host, backupPath string) error {
	ctx := context.Background()

	envCfg := service.TestEnv
	if env == model.Prod {
		envCfg = service.ProdEnv
	}

	if envCfg == nil {
		return errors.New("envConfig is nil")
	}

	// 停止服务
	_, err := exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: fmt.Sprintf("systemctl stop %s", service.ServiceName),
			Timeout: 30 * time.Second,
		},
	})
	if err != nil {
		fmt.Printf("警告: 停止服务失败: %v\n", err)
	}
	// 恢复备份
	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: fmt.Sprintf("rm -rf '%s' && mv '%s' '%s'", envCfg.InstallPath, backupPath, envCfg.InstallPath),
			Timeout: 60 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("恢复备份失败: %w", err)
	}

	// 启动服务
	_, err = exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: fmt.Sprintf("systemctl start %s", service.ServiceName),
			Timeout: 30 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("启动服务失败: %w", err)
	}

	fmt.Printf("服务器 %s 回滚成功\n", host)
	return nil
}

// createSystemdServiceFile 创建systemd service文件
func createSystemdServiceFile(exec *executor.Executor, service *model.Service, host string, env model.Env) error {
	envCfg := service.TestEnv
	if env == model.Prod {
		envCfg = service.ProdEnv
	}

	if envCfg == nil {
		return errors.New("envConfig is nil")
	}
	// 构建ExecStart命令
	execStart := service.StartCommand
	if execStart == "" {
		// 如果没有指定启动命令，使用默认的启动方式
		execStart = fmt.Sprintf("%s/start.sh", envCfg.InstallPath)
	} else {
		execStart = strings.ReplaceAll(execStart, "${env}", string(env))
		execStart = strings.ReplaceAll(execStart, "${installPath}", envCfg.InstallPath)
	}

	// 创建systemd服务配置
	serviceConfig := systemd.ServiceConfig{
		ServiceName: service.ServiceName,
		Description: fmt.Sprintf("%s application service", service.ServiceName),
		ExecStart:   execStart,
		WorkingDir:  envCfg.InstallPath,
		User:        "root",
		Group:       "root",
		Restart:     "always",
		RestartSec:  "10",
		Environment: []string{},
	}

	// 调用systemd包的函数创建service文件
	return systemd.CreateSystemdService(exec, host, serviceConfig)
}

// StartService 启动服务
func StartService(service *model.Service, serverHost []string) error {
	logger.Separator()
	logger.Step(1, 1, "开始启动服务: %s", service.ServiceName)
	logger.Info("目标服务器: %v", serverHost)

	exec := executor.NewExecutor()
	ctx := context.Background()

	for i, host := range serverHost {
		logger.Step(i+1, len(serverHost), "启动服务器 %s 上的服务 %s", host, service.ServiceName)

		_, err := exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("systemctl start %s", service.ServiceName),
				Timeout: 60 * time.Second,
			},
		})
		if err != nil {
			logger.Error("启动服务器 %s 上的服务失败: %v", host, err)

			// 询问用户是否继续
			var choice string
			prompt := &survey.Select{
				Message: fmt.Sprintf("服务器 %s 启动失败，请选择操作:", host),
				Options: []string{"重试启动", "继续下一台", "停止操作"},
				Default: "重试启动",
			}
			survey.AskOne(prompt, &choice)

			switch choice {
			case "重试启动":
				logger.Info("🔄 重试启动...")
				_, err = exec.Execute(ctx, &executor.ExecuteRequest{
					ServerHost: host,
					Type:       executor.CommandTypeSingle,
					Command: &executor.Command{
						Command: fmt.Sprintf("systemctl start %s", service.ServiceName),
						Timeout: 60 * time.Second,
					},
				})
				if err != nil {
					logger.Error("重试启动失败: %v", err)
					return err
				}
			case "继续下一台":
				logger.Warning("跳过当前服务器，继续下一台")
				continue
			case "停止操作":
				logger.Warning("用户选择停止操作")
				return fmt.Errorf("用户选择停止操作")
			}
		}

		// 验证服务状态
		logger.Info("🔍 验证服务状态...")
		err = checkServiceStatus(exec, host, service.ServiceName, "active")
		if err != nil {
			logger.Warning("服务器 %s 上的服务状态检查失败: %v", host, err)
		} else {
			logger.Success("✅ 服务器 %s 上的服务启动成功", host)
		}
	}

	logger.Separator()
	logger.Success("🎉 所有服务器启动完成")
	return nil
}

// StopService 停止服务
func StopService(service *model.Service, serverHost []string) error {
	logger.Separator()
	logger.Step(1, 1, "开始停止服务: %s", service.ServiceName)
	logger.Info("目标服务器: %v", serverHost)

	exec := executor.NewExecutor()
	ctx := context.Background()

	for i, host := range serverHost {
		logger.Step(i+1, len(serverHost), "停止服务器 %s 上的服务 %s", host, service.ServiceName)

		_, err := exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("systemctl stop %s", service.ServiceName),
				Timeout: 60 * time.Second,
			},
		})
		if err != nil {
			fmt.Printf("停止服务器 %s 上的服务失败: %v\n", host, err)

			// 询问用户是否继续
			var choice string
			prompt := &survey.Select{
				Message: fmt.Sprintf("服务器 %s 停止失败，请选择操作:", host),
				Options: []string{"强制停止", "继续下一台", "停止操作"},
				Default: "强制停止",
			}
			survey.AskOne(prompt, &choice)

			switch choice {
			case "强制停止":
				_, err = exec.Execute(ctx, &executor.ExecuteRequest{
					ServerHost: host,
					Type:       executor.CommandTypeSingle,
					Command: &executor.Command{
						Command: fmt.Sprintf("systemctl kill %s", service.ServiceName),
						Timeout: 30 * time.Second,
					},
				})
				if err != nil {
					fmt.Printf("强制停止失败: %v\n", err)
					return err
				}
			case "继续下一台":
				continue
			case "停止操作":
				return fmt.Errorf("用户选择停止操作")
			}
		}

		// 验证服务状态
		err = checkServiceStatus(exec, host, service.ServiceName, "inactive")
		if err != nil {
			fmt.Printf("警告: 服务器 %s 上的服务状态检查失败: %v\n", host, err)
		} else {
			logger.Success("✅ 服务器 %s 上的服务停止成功", host)
		}
	}

	logger.Separator()
	logger.Success("🛑 所有服务器停止完成")
	return nil
}

// RestartService 重启服务
func RestartService(service *model.Service, serverHost []string) error {
	fmt.Println("开始重启服务...")

	exec := executor.NewExecutor()
	ctx := context.Background()

	for _, host := range serverHost {
		fmt.Printf("重启服务器 %s 上的服务 %s\n", host, service.ServiceName)

		_, err := exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("systemctl restart %s", service.ServiceName),
				Timeout: 60 * time.Second,
			},
		})
		if err != nil {
			fmt.Printf("重启服务器 %s 上的服务失败: %v\n", host, err)

			// 询问用户是否继续
			var choice string
			prompt := &survey.Select{
				Message: fmt.Sprintf("服务器 %s 重启失败，请选择操作:", host),
				Options: []string{"重试重启", "手动处理", "继续下一台", "停止操作"},
				Default: "重试重启",
			}
			survey.AskOne(prompt, &choice)

			switch choice {
			case "重试重启":
				_, err = exec.Execute(ctx, &executor.ExecuteRequest{
					ServerHost: host,
					Type:       executor.CommandTypeSingle,
					Command: &executor.Command{
						Command: fmt.Sprintf("systemctl restart %s", service.ServiceName),
						Timeout: 60 * time.Second,
					},
				})
				if err != nil {
					fmt.Printf("重试重启失败: %v\n", err)
					return err
				}
			case "手动处理":
				// 先停止再启动
				fmt.Printf("尝试手动停止再启动服务...\n")
				exec.Execute(ctx, &executor.ExecuteRequest{
					ServerHost: host,
					Type:       executor.CommandTypeSingle,
					Command: &executor.Command{
						Command: fmt.Sprintf("systemctl stop %s", service.ServiceName),
						Timeout: 30 * time.Second,
					},
				})
				time.Sleep(2 * time.Second) // 等待2秒
				_, err = exec.Execute(ctx, &executor.ExecuteRequest{
					ServerHost: host,
					Type:       executor.CommandTypeSingle,
					Command: &executor.Command{
						Command: fmt.Sprintf("systemctl start %s", service.ServiceName),
						Timeout: 30 * time.Second,
					},
				})
				if err != nil {
					fmt.Printf("手动处理失败: %v\n", err)
					return err
				}
			case "继续下一台":
				continue
			case "停止操作":
				return fmt.Errorf("用户选择停止操作")
			}
		}

		// 验证服务状态
		err = checkServiceStatus(exec, host, service.ServiceName, "active")
		if err != nil {
			fmt.Printf("警告: 服务器 %s 上的服务状态检查失败: %v\n", host, err)
		} else {
			logger.Success("✅ 服务器 %s 上的服务重启成功", host)
		}
	}

	logger.Separator()
	logger.Success("🔄 所有服务器重启完成")
	return nil
}

// GetServiceStatus 获取服务状态
func GetServiceStatus(service *model.Service, serverHost []string) error {
	logger.Separator()
	logger.Step(1, 1, "检查服务状态: %s", service.ServiceName)
	logger.Info("目标服务器: %v", serverHost)

	exec := executor.NewExecutor()
	ctx := context.Background()

	for i, host := range serverHost {
		logger.Step(i+1, len(serverHost), "检查服务器 %s 上的服务 %s 状态", host, service.ServiceName)

		result, err := exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("systemctl status %s --no-pager", service.ServiceName),
				Timeout: 30 * time.Second,
			},
		})
		if err != nil {
			logger.Error("获取服务器 %s 上的服务状态失败: %v", host, err)
			continue
		}

		if result.CommandResult != nil {
			logger.Info("服务器 %s 状态信息:", host)
			fmt.Printf("    %s%s│%s %s\n", log.ColorBlue, log.ColorBold, log.ColorReset, strings.ReplaceAll(result.CommandResult.Stdout, "\n", fmt.Sprintf("\n    %s%s│%s ", log.ColorBlue, log.ColorBold, log.ColorReset)))
			if result.CommandResult.Stderr != "" {
				logger.Warning("错误信息:")
				fmt.Printf("    %s%s│%s %s%s%s\n", log.ColorRed, log.ColorBold, log.ColorReset, log.ColorRed, strings.ReplaceAll(result.CommandResult.Stderr, "\n", fmt.Sprintf("\n    %s%s│%s %s", log.ColorRed, log.ColorBold, log.ColorReset, log.ColorRed)), log.ColorReset)
			}
		}
		logger.Separator()
	}

	logger.Success("🔍 服务状态检查完成")
	return nil
}

// checkServiceStatus 检查服务状态是否符合预期
func checkServiceStatus(exec *executor.Executor, host, serviceName, expectedStatus string) error {
	ctx := context.Background()

	// 等待服务状态稳定
	time.Sleep(3 * time.Second)

	result, err := exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: fmt.Sprintf("systemctl is-active %s", serviceName),
			Timeout: 15 * time.Second,
		},
	})
	if err != nil {
		return fmt.Errorf("检查服务状态失败: %w", err)
	}

	var actualStatus string
	if result.CommandResult != nil {
		actualStatus = strings.TrimSpace(result.CommandResult.Stdout)
	}
	if actualStatus != expectedStatus {
		return fmt.Errorf("服务状态不符合预期，期望: %s, 实际: %s", expectedStatus, actualStatus)
	}

	return nil
}

// EnableService 启用服务开机自启
func EnableService(service *model.Service, serverHost []string) error {
	fmt.Println("启用服务开机自启...")

	exec := executor.NewExecutor()
	ctx := context.Background()

	for _, host := range serverHost {
		fmt.Printf("启用服务器 %s 上的服务 %s 开机自启\n", host, service.ServiceName)

		_, err := exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("systemctl enable %s", service.ServiceName),
				Timeout: 30 * time.Second,
			},
		})
		if err != nil {
			fmt.Printf("启用服务器 %s 上的服务开机自启失败: %v\n", host, err)
			return err
		}

		fmt.Printf("服务器 %s 上的服务开机自启设置成功\n", host)
	}

	fmt.Println("所有服务器开机自启设置完成")
	return nil
}

// DisableService 禁用服务开机自启
func DisableService(service *model.Service, serverHost []string) error {
	fmt.Println("禁用服务开机自启...")

	exec := executor.NewExecutor()
	ctx := context.Background()

	for _, host := range serverHost {
		fmt.Printf("禁用服务器 %s 上的服务 %s 开机自启\n", host, service.ServiceName)

		_, err := exec.Execute(ctx, &executor.ExecuteRequest{
			ServerHost: host,
			Type:       executor.CommandTypeSingle,
			Command: &executor.Command{
				Command: fmt.Sprintf("systemctl disable %s", service.ServiceName),
				Timeout: 30 * time.Second,
			},
		})
		if err != nil {
			fmt.Printf("禁用服务器 %s 上的服务开机自启失败: %v\n", host, err)
			return err
		}

		fmt.Printf("服务器 %s 上的服务开机自启禁用成功\n", host)
	}

	fmt.Println("所有服务器开机自启禁用完成")
	return nil
}

// Uninstall 一键卸载服务
func Uninstall(service *model.Service, env model.Env, serverHost []string) error {
	logger.Separator()
	logger.Step(1, 5, "开始卸载流程 - 服务: %s, 环境: %s", service.ServiceName, env)
	logger.Warning("⚠️  卸载操作将删除所有相关配置和数据，不可恢复！")

	// 询问用户确认卸载操作
	var confirmUninstall bool
	confirmPrompt := &survey.Confirm{
		Message: fmt.Sprintf("确定要卸载服务 %s 吗？此操作将删除所有相关配置和数据，不可恢复！", service.ServiceName),
		Default: false,
	}
	err := survey.AskOne(confirmPrompt, &confirmUninstall)
	if err != nil {
		logger.Error("获取用户确认失败: %v", err)
		return fmt.Errorf("获取用户确认失败: %w", err)
	}

	if !confirmUninstall {
		logger.Warning("用户取消卸载操作")
		return nil
	}

	exec := executor.NewExecutor()

	// 第一步：停止和禁用systemd服务
	logger.Step(2, 5, "停止并禁用systemd服务...")
	for _, host := range serverHost {
		logger.Progress("处理服务器: %s", host)
		err = systemd.UninstallSystemdService(exec, service.ServiceName, host)
		if err != nil {
			logger.Warning("服务器 %s 卸载systemd服务失败: %v", host, err)
		} else {
			logger.Success("服务器 %s systemd服务卸载成功", host)
		}
	}

	// 第二步：删除应用目录和文件
	logger.Step(3, 5, "删除应用目录和文件...")
	for _, host := range serverHost {
		logger.Progress("删除服务器 %s 上的应用文件", host)
		err = removeApplicationFiles(exec, service, env, host)
		if err != nil {
			logger.Warning("服务器 %s 删除应用文件失败: %v", host, err)
		} else {
			logger.Success("服务器 %s 应用文件删除成功", host)
		}
	}

	// 第三步：删除nginx配置
	logger.Step(4, 5, "删除nginx配置...")
	err = nginx.RemoveNginxConfig(service, env, cfg.NginxServers, cfg.NginxConfDir)
	if err != nil {
		logger.Warning("删除nginx配置失败: %v", err)
	} else {
		logger.Success("nginx配置删除成功")
	}

	// 第四步：删除DNS记录
	logger.Step(5, 5, "删除DNS记录...")
	err = dns.RemoveDNSRecord(service, env, cfg.DNSKey, cfg.DNSSecret)
	if err != nil {
		logger.Warning("删除DNS记录失败: %v", err)
	} else {
		logger.Success("DNS记录删除成功")
	}

	// 第五步：清理版本文件
	//logger.Info("🧹 清理版本文件...")
	//err = cleanupVersionFiles(service.ServiceName)
	//if err != nil {
	//	logger.Warning("清理版本文件失败: %v", err)
	//} else {
	//	logger.Success("版本文件清理成功")
	//}

	logger.Separator()
	logger.Success("🗑️ 卸载流程完成")
	return nil
}

// removeApplicationFiles 删除应用目录和文件
func removeApplicationFiles(exec *executor.Executor, service *model.Service, env model.Env, host string) error {
	ctx := context.Background()

	envCfg := service.TestEnv
	if env == model.Prod {
		envCfg = service.ProdEnv
	}

	if envCfg == nil {
		return errors.New("envConfig is nil")
	}

	logger.Progress("删除服务器 %s 上的应用文件: %s", host, envCfg.InstallPath)

	// 删除安装目录
	_, err := exec.Execute(ctx, &executor.ExecuteRequest{
		ServerHost: host,
		Type:       executor.CommandTypeSingle,
		Command: &executor.Command{
			Command: fmt.Sprintf("rm -rf '%s'", envCfg.InstallPath),
			Timeout: 60 * time.Second,
		},
	})
	if err != nil {
		logger.Error("删除安装目录失败: %v", err)
		return fmt.Errorf("删除安装目录失败: %w", err)
	}

	logger.Success("服务器 %s 上的应用文件删除成功", host)
	return nil
}

// cleanupVersionFiles 清理版本文件
func cleanupVersionFiles(serviceName string) error {
	versionsDir := filepath.Join(backupDir, serviceName)

	// 检查目录是否存在
	if _, err := os.Stat(versionsDir); os.IsNotExist(err) {
		logger.Info("版本目录不存在，跳过清理: %s", versionsDir)
		return nil
	}

	logger.Progress("删除版本目录: %s", versionsDir)
	// 删除版本目录
	err := os.RemoveAll(versionsDir)
	if err != nil {
		logger.Error("删除版本目录失败: %v", err)
		return fmt.Errorf("删除版本目录失败: %w", err)
	}

	logger.Success("版本文件清理成功: %s", versionsDir)
	return nil
}
