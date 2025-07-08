package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// 定义颜色代码
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"
)

var credentialContent []byte

func init() {
	file, err := os.ReadFile("/Users/xushixin/.ssh/id_rsa")
	if err != nil {
		panic(err)
	}
	credentialContent = file
}

// Executor 命令执行器实现
type Executor struct {
	logger *zap.Logger

	// 异步执行管理
	asyncResults sync.Map // requestID -> *ExecuteResult
	cancelFuncs  sync.Map // requestID -> context.CancelFunc
}

// Config 执行器配置
type Config struct {
	Logger *zap.Logger
}

// NewExecutor 创建命令执行器
func NewExecutor() *Executor {
	logger, _ := zap.NewProduction()
	return &Executor{
		logger: logger,
	}
}

// printCommand 打印命令执行日志
func (e *Executor) printCommand(serverHost, command string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	prefix := fmt.Sprintf("%s[%s CMD]%s", ColorPurple, timestamp, ColorReset)
	fmt.Printf("%s %s@%s %s%s$ %s%s\n", prefix, ColorPurple, serverHost, ColorPurple, ColorBold, command, ColorReset)
}

// Execute 执行命令
func (e *Executor) Execute(ctx context.Context, req *ExecuteRequest) (*ExecuteResult, error) {
	if req.ServerHost == "" {
		return nil, fmt.Errorf("服务器ID不能为空")
	}

	// 生成请求ID
	requestID := uuid.New().String()
	startTime := time.Now()

	result := &ExecuteResult{
		RequestID:  requestID,
		Type:       req.Type,
		ServerHost: req.ServerHost,
		Async:      false,
		StartTime:  startTime,
	}

	// 建立SSH连接
	sshClient, err := e.createSSHClient(ctx, req.ServerHost)
	if err != nil {
		return nil, fmt.Errorf("建立SSH连接失败: %w", err)
	}
	defer sshClient.Close()

	// 根据命令类型执行
	switch req.Type {
	case CommandTypeSingle:
		if req.Command == nil {
			return nil, fmt.Errorf("单个命令不能为空")
		}
		commandResult, err := e.executeSingleCommand(ctx, sshClient, req.Command)
		if err != nil {
			return nil, err
		}
		result.CommandResult = commandResult

	case CommandTypeBatch:
		if req.BatchCommand == nil {
			return nil, fmt.Errorf("批量命令不能为空")
		}
		batchResult, err := e.executeBatchCommand(ctx, sshClient, req.BatchCommand)
		if err != nil {
			return nil, err
		}
		result.BatchResult = batchResult

	default:
		return nil, fmt.Errorf("不支持的命令类型: %s", req.Type)
	}

	result.EndTime = time.Now()

	return result, nil
}

// ExecuteAsync 异步执行命令
func (e *Executor) ExecuteAsync(ctx context.Context, req *ExecuteRequest) (string, error) {
	requestID := uuid.New().String()

	// 创建可取消的上下文
	asyncCtx, cancel := context.WithCancel(context.Background())
	e.cancelFuncs.Store(requestID, cancel)

	// 启动异步执行
	go func() {
		defer func() {
			e.cancelFuncs.Delete(requestID)
			cancel()
		}()

		// 复制请求并设置为异步
		asyncReq := *req
		asyncReq.Async = true

		result, err := e.Execute(asyncCtx, &asyncReq)
		if err != nil {
			// 创建错误结果
			result = &ExecuteResult{
				RequestID:  requestID,
				Type:       req.Type,
				ServerHost: req.ServerHost,
				Async:      true,
				StartTime:  time.Now(),
				EndTime:    time.Now(),
			}

			if req.Type == CommandTypeSingle {
				result.CommandResult = &CommandResult{
					Status: CommandStatusFailed,
					Error:  err.Error(),
				}
			} else {
				result.BatchResult = &BatchResult{
					Status: CommandStatusFailed,
					Error:  err.Error(),
				}
			}
		}

		e.asyncResults.Store(requestID, result)
	}()

	return requestID, nil
}

// GetAsyncResult 获取异步执行结果
func (e *Executor) GetAsyncResult(ctx context.Context, requestID string) (*ExecuteResult, error) {
	if requestID == "" {
		return nil, fmt.Errorf("请求ID不能为空")
	}

	// 先从内存中查找
	if result, ok := e.asyncResults.Load(requestID); ok {
		return result.(*ExecuteResult), nil
	}

	return nil, fmt.Errorf("未找到请求ID为 %s 的执行结果", requestID)
}

// CancelExecution 取消执行
func (e *Executor) CancelExecution(ctx context.Context, requestID string) error {
	if requestID == "" {
		return fmt.Errorf("请求ID不能为空")
	}

	if cancel, ok := e.cancelFuncs.Load(requestID); ok {
		cancel.(context.CancelFunc)()
		e.cancelFuncs.Delete(requestID)
		return nil
	}

	return fmt.Errorf("未找到请求ID为 %s 的执行任务", requestID)
}

// createSSHClient 创建SSH客户端
func (e *Executor) createSSHClient(ctx context.Context, server string) (*ssh.Client, error) {
	signer, err := ssh.ParsePrivateKey([]byte(credentialContent))
	if err != nil {
		return nil, fmt.Errorf("解析SSH私钥失败: %w", err)
	}

	// 配置SSH客户端
	config := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	// 连接SSH服务器
	addr := fmt.Sprintf("%s:%d", server, 22)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("SSH连接失败: %w", err)
	}

	return client, nil
}

// executeSingleCommand 执行单个命令
func (e *Executor) executeSingleCommand(ctx context.Context, client *ssh.Client, cmd *Command) (*CommandResult, error) {
	result := &CommandResult{
		CommandID:   cmd.ID,
		CommandName: cmd.Name,
		Command:     cmd.Command,
		Status:      CommandStatusPending,
		StartTime:   time.Now(),
	}

	// 检查执行条件
	if cmd.Condition != "" {
		conditionMet, err := e.checkCondition(ctx, client, cmd.Condition)
		if err != nil {
			result.Status = CommandStatusFailed
			result.Error = fmt.Sprintf("检查执行条件失败: %v", err)
			result.EndTime = time.Now()
			result.Duration = result.EndTime.Sub(result.StartTime)
			return result, nil
		}

		if !conditionMet {
			result.Status = CommandStatusSuccess
			result.Stdout = "条件不满足，跳过执行"
			result.EndTime = time.Now()
			result.Duration = result.EndTime.Sub(result.StartTime)
			return result, nil
		}
	}

	// 执行命令（带重试）
	for attempt := 0; attempt <= cmd.RetryTimes; attempt++ {
		if attempt > 0 {
			result.RetryCount = attempt
			if cmd.RetryInterval > 0 {
				time.Sleep(cmd.RetryInterval)
			}
		}

		err := e.runCommand(ctx, client, cmd, result)
		if err == nil && result.ExitCode == 0 {
			result.Status = CommandStatusSuccess
			break
		}

		if attempt == cmd.RetryTimes {
			if cmd.IgnoreError {
				result.Status = CommandStatusSuccess
			} else {
				result.Status = CommandStatusFailed
			}
		}
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	return result, nil
}

// executeBatchCommand 执行批量命令
func (e *Executor) executeBatchCommand(ctx context.Context, client *ssh.Client, batchCmd *BatchCommand) (*BatchResult, error) {
	result := &BatchResult{
		BatchID:   batchCmd.ID,
		BatchName: batchCmd.Name,
		Status:    CommandStatusRunning,
		StartTime: time.Now(),
	}

	// 执行Try阶段
	trySuccess := true
	if len(batchCmd.TryCommands) > 0 {
		e.logger.Info("开始执行Try阶段", zap.String("batchID", batchCmd.ID))
		tryResults, err := e.executeCommandList(ctx, client, batchCmd.TryCommands, batchCmd.Mode, batchCmd.StopOnError)
		if err != nil {
			result.Error = fmt.Sprintf("Try阶段执行失败: %v", err)
			trySuccess = false
		}
		result.TryResults = tryResults

		// 检查Try阶段是否有失败的命令
		for _, cmdResult := range tryResults {
			result.TotalCommands++
			if cmdResult.Status == CommandStatusSuccess {
				result.SuccessCommands++
			} else {
				result.FailedCommands++
				trySuccess = false
			}
		}
	}

	// 如果Try阶段失败，执行Catch阶段
	if !trySuccess && len(batchCmd.CatchCommands) > 0 {
		e.logger.Info("Try阶段失败，开始执行Catch阶段", zap.String("batchID", batchCmd.ID))
		catchResults, err := e.executeCommandList(ctx, client, batchCmd.CatchCommands, batchCmd.Mode, false)
		if err != nil {
			e.logger.Error("Catch阶段执行失败", zap.Error(err))
		}
		result.CatchResults = catchResults

		for _, cmdResult := range catchResults {
			result.TotalCommands++
			if cmdResult.Status == CommandStatusSuccess {
				result.SuccessCommands++
			} else {
				result.FailedCommands++
			}
		}
	}

	// 执行Finally阶段
	if len(batchCmd.FinallyCommands) > 0 {
		e.logger.Info("开始执行Finally阶段", zap.String("batchID", batchCmd.ID))
		finallyResults, err := e.executeCommandList(ctx, client, batchCmd.FinallyCommands, batchCmd.Mode, false)
		if err != nil {
			e.logger.Error("Finally阶段执行失败", zap.Error(err))
		}
		result.FinallyResults = finallyResults

		for _, cmdResult := range finallyResults {
			result.TotalCommands++
			if cmdResult.Status == CommandStatusSuccess {
				result.SuccessCommands++
			} else {
				result.FailedCommands++
			}
		}
	}

	// 确定整体状态
	if result.FailedCommands == 0 {
		result.Status = CommandStatusSuccess
	} else if trySuccess || batchCmd.ContinueOnFailed {
		result.Status = CommandStatusSuccess // 部分成功
	} else {
		result.Status = CommandStatusFailed
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	return result, nil
}

// executeCommandList 执行命令列表
func (e *Executor) executeCommandList(ctx context.Context, client *ssh.Client, commands []*Command, mode BatchMode, stopOnError bool) ([]*CommandResult, error) {
	results := make([]*CommandResult, 0, len(commands))

	if mode == BatchModeParallel {
		// 并行执行
		resultsChan := make(chan *CommandResult, len(commands))
		var wg sync.WaitGroup

		for _, cmd := range commands {
			wg.Add(1)
			go func(command *Command) {
				defer wg.Done()
				result, err := e.executeSingleCommand(ctx, client, command)
				if err != nil {
					result = &CommandResult{
						CommandID:   command.ID,
						CommandName: command.Name,
						Command:     command.Command,
						Status:      CommandStatusFailed,
						Error:       err.Error(),
						StartTime:   time.Now(),
						EndTime:     time.Now(),
					}
				}
				resultsChan <- result
			}(cmd)
		}

		go func() {
			wg.Wait()
			close(resultsChan)
		}()

		for result := range resultsChan {
			results = append(results, result)
		}

	} else {
		// 顺序执行
		for _, cmd := range commands {
			result, err := e.executeSingleCommand(ctx, client, cmd)
			if err != nil {
				result = &CommandResult{
					CommandID:   cmd.ID,
					CommandName: cmd.Name,
					Command:     cmd.Command,
					Status:      CommandStatusFailed,
					Error:       err.Error(),
					StartTime:   time.Now(),
					EndTime:     time.Now(),
				}
			}

			results = append(results, result)

			// 如果设置了遇错停止且当前命令失败
			if stopOnError && result.Status == CommandStatusFailed && !cmd.ContinueOnError {
				break
			}
		}
	}

	return results, nil
}

// checkCondition 检查执行条件
func (e *Executor) checkCondition(ctx context.Context, client *ssh.Client, condition string) (bool, error) {
	session, err := client.NewSession()
	if err != nil {
		return false, err
	}
	defer session.Close()

	// 打印要执行的条件检查命令
	e.printCommand(client.RemoteAddr().String(), condition)

	// 执行条件检查命令
	err = session.Run(condition)
	return err == nil, nil
}

// runCommand 运行单个命令
func (e *Executor) runCommand(ctx context.Context, client *ssh.Client, cmd *Command, result *CommandResult) error {
	result.Status = CommandStatusRunning

	session, err := client.NewSession()
	if err != nil {
		result.Error = fmt.Sprintf("创建SSH会话失败: %v", err)
		return err
	}
	defer session.Close()

	// 设置工作目录和环境变量
	var command strings.Builder

	if cmd.WorkDir != "" {
		command.WriteString(fmt.Sprintf("cd %s && ", cmd.WorkDir))
	}

	if len(cmd.Environment) > 0 {
		for key, value := range cmd.Environment {
			command.WriteString(fmt.Sprintf("export %s=%s && ", key, value))
		}
	}

	command.WriteString(cmd.Command)

	// 打印要执行的命令
	e.printCommand(client.RemoteAddr().String(), command.String())

	// 创建管道获取输出
	stdout, err := session.StdoutPipe()
	if err != nil {
		result.Error = fmt.Sprintf("创建stdout管道失败: %v", err)
		return err
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		result.Error = fmt.Sprintf("创建stderr管道失败: %v", err)
		return err
	}

	// 启动命令
	err = session.Start(command.String())
	if err != nil {
		result.Error = fmt.Sprintf("启动命令失败: %v", err)
		return err
	}

	// 读取输出
	stdoutData := make([]byte, 0)
	stderrData := make([]byte, 0)

	done := make(chan error, 1)
	go func() {
		done <- session.Wait()
	}()

	// 设置超时
	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute // 默认5分钟超时
	}

	var waitErr error
	select {
	case waitErr = <-done:
		// 命令正常结束
	case <-time.After(timeout):
		// 超时
		session.Signal(ssh.SIGTERM)
		result.Status = CommandStatusTimeout
		result.Error = "命令执行超时"
		return fmt.Errorf("命令执行超时")
	case <-ctx.Done():
		// 上下文取消
		session.Signal(ssh.SIGTERM)
		result.Status = CommandStatusCancelled
		result.Error = "命令执行被取消"
		return ctx.Err()
	}

	// 读取所有输出
	buf := make([]byte, 1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			stdoutData = append(stdoutData, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			stderrData = append(stderrData, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	result.Stdout = string(stdoutData)
	result.Stderr = string(stderrData)

	// 获取退出码
	if waitErr != nil {
		if exitError, ok := waitErr.(*exec.ExitError); ok {
			result.ExitCode = exitError.ExitCode()
		} else {
			result.ExitCode = 1
		}
		result.Error = waitErr.Error()
	} else {
		result.ExitCode = 0
	}

	return nil
}

// TestConnection 测试服务器连接
func (e *Executor) TestConnection(ctx context.Context, req *TestConnectionRequest) (*TestConnectionResult, error) {
	if req.Host == "" {
		return nil, fmt.Errorf("主机地址不能为空")
	}
	if req.Username == "" {
		return nil, fmt.Errorf("用户名不能为空")
	}
	if req.CredentialID == "" {
		return nil, fmt.Errorf("密钥ID不能为空")
	}

	// 设置默认端口
	port := req.Port
	if port <= 0 {
		port = 22
	}

	return e.testSSHKeyConnection(req.Host, port, req.Username, credentialContent)
}

// testSSHKeyConnection 测试SSH密钥连接
func (e *Executor) testSSHKeyConnection(host string, port int, username string, privateKey []byte) (*TestConnectionResult, error) {
	start := time.Now()

	// 解析私钥
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return &TestConnectionResult{
			Success: false,
			Message: "解析SSH私钥失败",
			Error:   err.Error(),
			Latency: time.Since(start).Milliseconds(),
		}, nil
	}

	// 配置SSH客户端
	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	// 连接SSH服务器
	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return &TestConnectionResult{
			Success: false,
			Message: "SSH连接失败",
			Error:   err.Error(),
			Latency: time.Since(start).Milliseconds(),
		}, nil
	}
	defer client.Close()

	// 执行简单命令测试
	session, err := client.NewSession()
	if err != nil {
		return &TestConnectionResult{
			Success: false,
			Message: "创建SSH会话失败",
			Error:   err.Error(),
			Latency: time.Since(start).Milliseconds(),
		}, nil
	}
	defer session.Close()

	// 打印要执行的测试命令
	e.printCommand(client.RemoteAddr().String(), "echo 'test'")

	err = session.Run("echo 'test'")
	if err != nil {
		return &TestConnectionResult{
			Success: false,
			Message: "执行测试命令失败",
			Error:   err.Error(),
			Latency: time.Since(start).Milliseconds(),
		}, nil
	}

	return &TestConnectionResult{
		Success: true,
		Message: "SSH连接测试成功",
		Latency: time.Since(start).Milliseconds(),
	}, nil
}

// testPasswordConnection 测试密码连接
func (e *Executor) testPasswordConnection(host string, port int, username, password string) (*TestConnectionResult, error) {
	start := time.Now()

	// 配置SSH客户端
	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	// 连接SSH服务器
	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return &TestConnectionResult{
			Success: false,
			Message: "SSH连接失败",
			Error:   err.Error(),
			Latency: time.Since(start).Milliseconds(),
		}, nil
	}
	defer client.Close()

	// 执行简单命令测试
	session, err := client.NewSession()
	if err != nil {
		return &TestConnectionResult{
			Success: false,
			Message: "创建SSH会话失败",
			Error:   err.Error(),
			Latency: time.Since(start).Milliseconds(),
		}, nil
	}
	defer session.Close()

	// 打印要执行的测试命令
	e.printCommand(client.RemoteAddr().String(), "echo 'test'")

	err = session.Run("echo 'test'")
	if err != nil {
		return &TestConnectionResult{
			Success: false,
			Message: "执行测试命令失败",
			Error:   err.Error(),
			Latency: time.Since(start).Milliseconds(),
		}, nil
	}

	return &TestConnectionResult{
		Success: true,
		Message: "SSH连接测试成功",
		Latency: time.Since(start).Milliseconds(),
	}, nil
}

// uploadSSHKey 上传SSH密钥到服务器
func (e *Executor) uploadSSHKey(client *ssh.Client, keyContent, remotePath string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建SSH会话失败: %w", err)
	}
	defer session.Close()

	// 使用cat命令写入SSH密钥
	cmd := fmt.Sprintf("cat > %s && chmod 600 %s", remotePath, remotePath)

	// 打印要执行的SSH密钥上传命令
	e.printCommand(client.RemoteAddr().String(), cmd)

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("创建stdin管道失败: %w", err)
	}

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("启动命令失败: %w", err)
	}

	// 写入密钥内容
	if _, err := stdin.Write([]byte(keyContent)); err != nil {
		return fmt.Errorf("写入密钥内容失败: %w", err)
	}
	stdin.Close()

	if err := session.Wait(); err != nil {
		return fmt.Errorf("上传SSH密钥失败: %w", err)
	}

	return nil
}

// uploadTextFile 上传文本文件到服务器
func (e *Executor) uploadTextFile(client *ssh.Client, content, remotePath string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建SSH会话失败: %w", err)
	}
	defer session.Close()

	cmd := fmt.Sprintf("cat > %s", remotePath)

	// 打印要执行的文件上传命令
	e.printCommand(client.RemoteAddr().String(), cmd)

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("创建stdin管道失败: %w", err)
	}

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("启动命令失败: %w", err)
	}

	if _, err := stdin.Write([]byte(content)); err != nil {
		return fmt.Errorf("写入文件内容失败: %w", err)
	}
	stdin.Close()

	if err := session.Wait(); err != nil {
		return fmt.Errorf("上传文件失败: %w", err)
	}

	return nil
}

// cleanupTempFile 清理临时文件
func (e *Executor) cleanupTempFile(client *ssh.Client, filePath string) {
	session, err := client.NewSession()
	if err != nil {
		e.logger.Warn("创建清理会话失败", zap.String("file", filePath), zap.Error(err))
		return
	}
	defer session.Close()

	cmd := fmt.Sprintf("rm -f %s", filePath)

	// 打印要执行的清理命令
	e.printCommand(client.RemoteAddr().String(), cmd)

	if err := session.Run(cmd); err != nil {
		e.logger.Warn("清理临时文件失败", zap.String("file", filePath), zap.Error(err))
	}
}

// extractGitHost 从Git URL中提取主机名
func (e *Executor) extractGitHost(repoURL string) string {
	// 处理SSH格式: git@github.com:user/repo.git
	if strings.HasPrefix(repoURL, "git@") {
		parts := strings.Split(repoURL, "@")
		if len(parts) >= 2 {
			hostAndPath := parts[1]
			colonIndex := strings.Index(hostAndPath, ":")
			if colonIndex > 0 {
				return hostAndPath[:colonIndex]
			}
		}
	}

	// 处理HTTPS格式: https://github.com/user/repo.git
	if strings.HasPrefix(repoURL, "https://") {
		repoURL = strings.TrimPrefix(repoURL, "https://")
		slashIndex := strings.Index(repoURL, "/")
		if slashIndex > 0 {
			return repoURL[:slashIndex]
		}
	}

	// 默认返回github.com
	return "github.com"
}

// modifyGitURLForSSHConfig 修改Git URL以使用SSH配置
func (e *Executor) modifyGitURLForSSHConfig(repoURL string) string {
	// 如果是SSH格式，替换主机名为配置中的别名
	if strings.HasPrefix(repoURL, "git@") {
		parts := strings.Split(repoURL, "@")
		if len(parts) >= 2 {
			hostAndPath := parts[1]
			colonIndex := strings.Index(hostAndPath, ":")
			if colonIndex > 0 {
				path := hostAndPath[colonIndex:]
				return "git@git-clone-host" + path
			}
		}
	}

	return repoURL
}

// runGitCommand 运行Git相关命令
func (e *Executor) runGitCommand(ctx context.Context, client *ssh.Client, command string, result *CommandResult, timeout time.Duration) error {
	result.Status = CommandStatusRunning

	session, err := client.NewSession()
	if err != nil {
		result.Error = fmt.Sprintf("创建SSH会话失败: %v", err)
		result.Status = CommandStatusFailed
		return err
	}
	defer session.Close()

	// 创建管道获取输出
	stdout, err := session.StdoutPipe()
	if err != nil {
		result.Error = fmt.Sprintf("创建stdout管道失败: %v", err)
		result.Status = CommandStatusFailed
		return err
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		result.Error = fmt.Sprintf("创建stderr管道失败: %v", err)
		result.Status = CommandStatusFailed
		return err
	}

	// 打印要执行的Git命令
	e.printCommand("Git", command)

	// 启动命令
	err = session.Start(command)
	if err != nil {
		result.Error = fmt.Sprintf("启动命令失败: %v", err)
		result.Status = CommandStatusFailed
		return err
	}

	// 读取输出
	stdoutData := make([]byte, 0)
	stderrData := make([]byte, 0)

	done := make(chan error, 1)
	go func() {
		done <- session.Wait()
	}()

	// 设置超时
	if timeout <= 0 {
		timeout = 10 * time.Minute // 默认10分钟超时
	}

	var waitErr error
	select {
	case waitErr = <-done:
		// 命令正常结束
	case <-time.After(timeout):
		// 超时
		session.Signal(ssh.SIGTERM)
		result.Status = CommandStatusTimeout
		result.Error = "Git命令执行超时"
		return fmt.Errorf("Git命令执行超时")
	case <-ctx.Done():
		// 上下文取消
		session.Signal(ssh.SIGTERM)
		result.Status = CommandStatusCancelled
		result.Error = "Git命令执行被取消"
		return ctx.Err()
	}

	// 读取所有输出
	buf := make([]byte, 1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			stdoutData = append(stdoutData, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			stderrData = append(stderrData, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	result.Stdout = string(stdoutData)
	result.Stderr = string(stderrData)
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	// 获取退出码
	if waitErr != nil {
		if exitError, ok := waitErr.(*ssh.ExitError); ok {
			result.ExitCode = exitError.ExitStatus()
		} else {
			result.ExitCode = 1
		}
		result.Error = waitErr.Error()
		result.Status = CommandStatusFailed
	} else {
		result.ExitCode = 0
		result.Status = CommandStatusSuccess
	}

	return nil
}
