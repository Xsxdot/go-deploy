package executor

import "time"

// CommandType 命令类型
type CommandType string

const (
	CommandTypeSingle CommandType = "single" // 单个命令
	CommandTypeBatch  CommandType = "batch"  // 批量命令
)

// CommandStatus 命令状态
type CommandStatus string

const (
	CommandStatusPending   CommandStatus = "pending"   // 等待执行
	CommandStatusRunning   CommandStatus = "running"   // 正在执行
	CommandStatusSuccess   CommandStatus = "success"   // 执行成功
	CommandStatusFailed    CommandStatus = "failed"    // 执行失败
	CommandStatusCancelled CommandStatus = "cancelled" // 已取消
	CommandStatusTimeout   CommandStatus = "timeout"   // 超时
)

// BatchMode 批量执行模式
type BatchMode string

const (
	BatchModeSequential BatchMode = "sequential" // 顺序执行
	BatchModeParallel   BatchMode = "parallel"   // 并行执行
)

// Command 单个命令定义
type Command struct {
	// 基本信息
	ID          string            `json:"id"`          // 命令唯一标识
	Name        string            `json:"name"`        // 命令名称
	Command     string            `json:"command"`     // 实际执行的命令
	WorkDir     string            `json:"work_dir"`    // 工作目录
	Environment map[string]string `json:"environment"` // 环境变量

	// 执行配置
	Timeout         time.Duration `json:"timeout"`           // 超时时间
	IgnoreError     bool          `json:"ignore_error"`      // 是否忽略错误
	ContinueOnError bool          `json:"continue_on_error"` // 错误时是否继续

	// 条件执行
	Condition string `json:"condition"` // 执行条件（shell表达式）

	// 重试配置
	RetryTimes    int           `json:"retry_times"`    // 重试次数
	RetryInterval time.Duration `json:"retry_interval"` // 重试间隔
}

// BatchCommand 批量命令定义
type BatchCommand struct {
	// 基本信息
	ID   string `json:"id"`   // 批量命令唯一标识
	Name string `json:"name"` // 批量命令名称

	// 执行模式
	Mode    BatchMode     `json:"mode"`    // 执行模式
	Timeout time.Duration `json:"timeout"` // 总超时时间

	// Try-Catch-Finally 结构
	TryCommands     []*Command `json:"try_commands"`     // 主要执行的命令
	CatchCommands   []*Command `json:"catch_commands"`   // 异常处理命令
	FinallyCommands []*Command `json:"finally_commands"` // 最终执行的命令

	// 执行策略
	StopOnError      bool `json:"stop_on_error"`      // 遇到错误是否停止
	ContinueOnFailed bool `json:"continue_on_failed"` // 失败时是否继续执行catch和finally
}

// ExecuteRequest 命令执行请求
type ExecuteRequest struct {
	// 目标服务器
	ServerHost string `json:"server_host"` // 服务器ID

	// 命令信息
	Type         CommandType   `json:"type"`          // 命令类型
	Command      *Command      `json:"command"`       // 单个命令（当type为single时）
	BatchCommand *BatchCommand `json:"batch_command"` // 批量命令（当type为batch时）

	// 执行配置
	Async   bool `json:"async"`    // 是否异步执行
	SaveLog bool `json:"save_log"` // 是否保存日志
}

// CommandResult 单个命令执行结果
type CommandResult struct {
	// 命令信息
	CommandID   string `json:"command_id"`   // 命令ID
	CommandName string `json:"command_name"` // 命令名称
	Command     string `json:"command"`      // 执行的命令

	// 执行状态
	Status    CommandStatus `json:"status"`     // 执行状态
	ExitCode  int           `json:"exit_code"`  // 退出码
	StartTime time.Time     `json:"start_time"` // 开始时间
	EndTime   time.Time     `json:"end_time"`   // 结束时间
	Duration  time.Duration `json:"duration"`   // 执行时长

	// 输出结果
	Stdout string `json:"stdout"` // 标准输出
	Stderr string `json:"stderr"` // 标准错误
	Error  string `json:"error"`  // 错误信息

	// 重试信息
	RetryCount int `json:"retry_count"` // 重试次数
}

// BatchResult 批量命令执行结果
type BatchResult struct {
	// 批量命令信息
	BatchID   string `json:"batch_id"`   // 批量命令ID
	BatchName string `json:"batch_name"` // 批量命令名称
	ServerID  string `json:"server_id"`  // 服务器ID

	// 执行状态
	Status    CommandStatus `json:"status"`     // 整体状态
	StartTime time.Time     `json:"start_time"` // 开始时间
	EndTime   time.Time     `json:"end_time"`   // 结束时间
	Duration  time.Duration `json:"duration"`   // 执行时长

	// 命令结果
	TryResults     []*CommandResult `json:"try_results"`     // Try阶段结果
	CatchResults   []*CommandResult `json:"catch_results"`   // Catch阶段结果
	FinallyResults []*CommandResult `json:"finally_results"` // Finally阶段结果

	// 统计信息
	TotalCommands   int `json:"total_commands"`   // 总命令数
	SuccessCommands int `json:"success_commands"` // 成功命令数
	FailedCommands  int `json:"failed_commands"`  // 失败命令数
	SkippedCommands int `json:"skipped_commands"` // 跳过命令数

	// 错误信息
	Error string `json:"error"` // 整体错误信息
}

// ExecuteResult 执行结果
type ExecuteResult struct {
	// 请求信息
	RequestID  string      `json:"request_id"`  // 请求ID
	Type       CommandType `json:"type"`        // 命令类型
	ServerHost string      `json:"server_host"` // 服务器ID

	// 结果
	CommandResult *CommandResult `json:"command_result"` // 单个命令结果
	BatchResult   *BatchResult   `json:"batch_result"`   // 批量命令结果

	// 执行信息
	Async     bool      `json:"async"`      // 是否异步执行
	StartTime time.Time `json:"start_time"` // 开始时间
	EndTime   time.Time `json:"end_time"`   // 结束时间
}

// TestConnectionRequest 测试连接请求
type TestConnectionRequest struct {
	Host         string `json:"host" validate:"required"`         // 主机地址
	Port         int    `json:"port" validate:"min=1,max=65535"`  // 端口
	Username     string `json:"username" validate:"required"`     // 用户名
	CredentialID string `json:"credentialId" validate:"required"` // 密钥ID
}

// TestConnectionResult 测试连接结果
type TestConnectionResult struct {
	Success bool   `json:"success"`         // 是否成功
	Message string `json:"message"`         // 结果消息
	Latency int64  `json:"latency"`         // 延迟（毫秒）
	Error   string `json:"error,omitempty"` // 错误信息
}
