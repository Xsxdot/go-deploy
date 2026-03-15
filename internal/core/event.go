package core

import "time"

// EventType 定义事件的性质
type EventType string

const (
	EventLog              EventType = "LOG"              // 普通文本日志 (如 SSH 输出)
	EventStatus           EventType = "STATUS"           // 状态流转 (如: 开始, 成功, 失败)
	EventProg             EventType = "PROG"             // 进度条更新 (如: 上传了 45%)
	EventApprovalWaiting  EventType = "APPROVAL_WAITING"  // manual_approval 等待用户 y/n 输入，通知 TUI 进入审批模式
)

// Event 结构化事件契约
type Event struct {
	Timestamp time.Time
	Type      EventType
	Level     string // INFO, WARN, ERROR
	StepName  string // 产生事件的流水线步骤
	TargetID  string // 产生事件的目标机器 (如果是本地逻辑则为空)
	Message   string // 具体内容
	Payload   any    // 扩展字段（比如传进度条的百分比数字，或具体的 error 对象）
}
