package tui

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	headerHeight = 4
	footerHeight = 1 // 底部仅一行快捷键提示
)

// stepStatus 步骤状态
type stepStatus struct {
	Status    string // pending, running, done, failed
	Message   string // 如 "Running...", "Done"
	Duration  time.Duration
	StartTime time.Time
}

// nodeState 节点级状态
type nodeState struct {
	Status   string // running, done, failed
	Message  string
	Progress int    // 0-100 for EventProg
	Speed    string // e.g. "12MB/s"
	ETA      string
}

// logEntry 日志条目
type logEntry struct {
	StepName  string
	TargetID  string
	Level     string
	Message   string
	Timestamp time.Time
}

// pipelineDoneMsg 流水线结束消息，由 Engine 或 main 触发
type pipelineDoneMsg struct {
	Err error
}

// Model Bubble Tea 模型
type Model struct {
	cfg          *core.PipelineConfig
	infra        *core.InfraConfig
	effectiveEnv string // 实际通过 CLI -e/--env 传入的环境名，用于 Header 展示
	order        []*core.Step
	stepStatus   map[string]*stepStatus
	nodeStatus   map[string]map[string]*nodeState
	logs         []logEntry
	mu           sync.RWMutex

	logViewport viewport.Model
	width       int
	height      int
	done        bool
	err         error

	// deployVersion、deployMessage 部署版本号和描述，用于成功完成时的输出
	deployVersion string
	deployMessage string

	// leftOffset 左栏步骤列表滚动偏移，用于 Auto-Tracking
	leftOffset int
	// autoScroll 右侧日志是否自动滚到底部
	autoScroll bool
	// leftPaneWidth, rightPaneWidth 由 WindowSizeMsg 计算（45% / 55%）
	leftPaneWidth  int
	rightPaneWidth int

	// cancelFunc 可选；Ctrl+C 时调用以触发 Engine context 取消，实现优雅熔断
	cancelFunc context.CancelFunc

	// approvalChan TUI 模式下 manual_approval 等待输入时，将用户按下的 y/n 发送到此 channel
	approvalChan chan<- string
	// waitingApproval 为 true 时，y/n 按键转发给 approvalChan 而非 viewport
	waitingApproval bool
}

// NewModel 创建 TUI 模型，order 来自 engine.ValidateDAG；env 为 CLI -e/--env 传入的环境名
// deployVersion 与 deployMessage 可选，用于 apply 成功完成时展示（destroy 等场景传空即可）
func NewModel(cfg *core.PipelineConfig, infra *core.InfraConfig, order []*core.Step, env, deployVersion, deployMessage string) Model {
	stMap := make(map[string]*stepStatus)
	for _, s := range order {
		stMap[s.Name] = &stepStatus{
			Status: "pending",
		}
	}

	bodyH := 24 - headerHeight - footerHeight
	if bodyH < 1 {
		bodyH = 1
	}
	vpH := bodyH - 1
	if vpH < 1 {
		vpH = 1
	}
	leftW := 80 * 45 / 100
	if leftW < 35 {
		leftW = 35
	}
	rightW := 80 - leftW - 2 // 预留边框
	if rightW < 20 {
		rightW = 20
	}
	if env == "" {
		env = "default"
	}
	vp := viewport.New(rightW, vpH)
	return Model{
		cfg:            cfg,
		infra:          infra,
		effectiveEnv:   env,
		order:          order,
		stepStatus:     stMap,
		nodeStatus:     make(map[string]map[string]*nodeState),
		logs:           make([]logEntry, 0, 256),
		logViewport:    vp,
		width:          80,
		height:         24,
		done:           false,
		err:            nil,
		deployVersion:  deployVersion,
		deployMessage:  deployMessage,
		leftOffset:     0,
		autoScroll:     true,
		leftPaneWidth:  leftW,
		rightPaneWidth: rightW,
	}
}

// Init 初始化
func (m Model) Init() tea.Cmd {
	return nil
}

// Update 处理消息
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		bodyHeight := msg.Height - headerHeight - footerHeight
		if bodyHeight < 1 {
			bodyHeight = 1
		}
		vpHeight := bodyHeight - 1 // 预留 1 行给右栏标题 "▼ Real-time Logs & Output"
		if vpHeight < 1 {
			vpHeight = 1
		}
		leftW := msg.Width * 45 / 100
		if leftW < 35 {
			leftW = 35
		}
		rightW := msg.Width - leftW - 2
		if rightW < 20 {
			rightW = 20
		}
		m.leftPaneWidth = leftW
		m.rightPaneWidth = rightW
		m.logViewport.Width = rightW
		m.logViewport.Height = vpHeight
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			if m.cancelFunc != nil {
				m.cancelFunc() // 触发 Engine context 取消，执行 Rollback 后优雅退出
			}
			return m, tea.Quit
		}
		// manual_approval 等待输入时，y/n 转发给 approvalChan
		if m.waitingApproval && m.approvalChan != nil {
			key := msg.String()
			if key == "y" || key == "n" {
				select {
				case m.approvalChan <- key:
					m.waitingApproval = false
				default:
				}
				return m, nil
			}
		}
		if msg.String() == "a" {
			m.autoScroll = !m.autoScroll
			if m.autoScroll {
				m.logViewport.GotoBottom()
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.logViewport, cmd = m.logViewport.Update(msg)
		return m, cmd

	case batchEventMsg:
		m.applyEvents(msg.Events)
		return m, nil

	case pipelineDoneMsg:
		m.done = true
		m.err = msg.Err
		return m, nil // 不自动退出，等待用户按 q 查看日志后退出

	default:
		var cmd tea.Cmd
		m.logViewport, cmd = m.logViewport.Update(msg)
		return m, cmd
	}
}

// applyEvents 应用批量事件到模型
func (m *Model) applyEvents(events []core.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range events {
		switch e.Type {
		case core.EventStatus:
			if st, ok := m.stepStatus[e.StepName]; ok {
				prevStatus := st.Status
				st.Status = statusFromMessage(e.Message)
				st.Message = e.Message
				if e.Message == "Running" || e.Message == "Uninstalling" || e.Message == "Done" || e.Message == "Failed" {
					if st.StartTime.IsZero() && (e.Message == "Running" || e.Message == "Uninstalling") {
						st.StartTime = e.Timestamp
					}
					if e.Message == "Done" || e.Message == "Failed" {
						if e.Payload != nil {
							if d, ok := e.Payload.(time.Duration); ok {
								st.Duration = d
							} else {
								st.Duration = safeDuration(e.Timestamp, st.StartTime)
							}
						} else {
							st.Duration = safeDuration(e.Timestamp, st.StartTime)
						}
					}
				}
				// Auto-Tracking: 当步骤变为 Running 时，滚动左栏使该步骤可见并尽量垂直居中
				if (e.Message == "Running" || e.Message == "Uninstalling") && prevStatus != "running" {
					m.updateLeftOffsetForStep(e.StepName)
				}
				// 当步骤完成（Done/Failed）时，将仍为 running 的子树节点标记为 done
				if e.Message == "Done" || e.Message == "Failed" {
					for _, ns := range m.nodeStatus[e.StepName] {
						if ns != nil && ns.Status == "running" {
							ns.Status = "done"
						}
					}
				}
			}
		case core.EventProg:
			if m.nodeStatus[e.StepName] == nil {
				m.nodeStatus[e.StepName] = make(map[string]*nodeState)
			}
			ns := m.nodeStatus[e.StepName][e.TargetID]
			if ns == nil {
				ns = &nodeState{Status: "running"}
				m.nodeStatus[e.StepName][e.TargetID] = ns
			}
			if e.Payload != nil {
				if pct, ok := e.Payload.(int); ok {
					ns.Progress = pct
				}
			}
			ns.Message = e.Message
		case core.EventApprovalWaiting:
			m.waitingApproval = true
		case core.EventLog:
			m.logs = append(m.logs, logEntry{
				StepName:  e.StepName,
				TargetID:  e.TargetID,
				Level:     e.Level,
				Message:   e.Message,
				Timestamp: e.Timestamp,
			})
			// 含 TargetID 的日志：创建/更新子树节点
			if e.TargetID != "" {
				if m.nodeStatus[e.StepName] == nil {
					m.nodeStatus[e.StepName] = make(map[string]*nodeState)
				}
				ns := m.nodeStatus[e.StepName][e.TargetID]
				if e.Level == "ERROR" {
					if ns == nil {
						m.nodeStatus[e.StepName][e.TargetID] = &nodeState{Status: "failed", Message: e.Message}
					} else {
						ns.Status = "failed"
						ns.Message = e.Message
					}
				} else {
					completed := isNodeCompletionLog(e.Message)
					if ns == nil {
						status := "running"
						if completed {
							status = "done"
						}
						m.nodeStatus[e.StepName][e.TargetID] = &nodeState{Status: status, Message: e.Message}
					} else if ns.Status != "failed" {
						ns.Message = e.Message
						if completed {
							ns.Status = "done"
						}
					}
				}
			}
			// 若无 EventStatus，从首条 Log 推断 step 开始
			if st, ok := m.stepStatus[e.StepName]; ok && st.Status == "pending" {
				st.Status = "running"
				st.Message = "Running..."
				st.StartTime = e.Timestamp
				m.updateLeftOffsetForStep(e.StepName)
			}
		}
	}

	// 更新 viewport 内容
	m.refreshLogViewport()
}

// safeDuration 计算 end - start，当 start 为零时返回 0，避免 time.Duration 溢出（int64 纳秒）
func safeDuration(end, start time.Time) time.Duration {
	if start.IsZero() {
		return 0
	}
	return end.Sub(start)
}

func statusFromMessage(msg string) string {
	switch msg {
	case "Done", "Success", "success":
		return "done"
	case "Failed", "Error", "error":
		return "failed"
	case "Running", "running":
		return "running"
	case "Waiting", "waiting":
		return "waiting"
	default:
		return "running"
	}
}

// isNodeCompletionLog 判断 EventLog 消息是否表明该 target 操作已完成
func isNodeCompletionLog(msg string) bool {
	s := strings.ToLower(msg)
	return strings.Contains(s, "done") ||
		strings.Contains(s, "deployed") ||
		strings.Contains(s, "ok:") ||
		strings.Contains(s, "reloaded") ||
		strings.Contains(s, "pruned:")
}

// stepLineIndex 返回步骤在左栏行列表中的起始行索引（0-based）。必须在持有 m.mu 时调用。
func (m *Model) stepLineIndex(stepName string) int {
	lineIdx := 0
	for _, step := range m.order {
		if step.Name == stepName {
			return lineIdx
		}
		st := m.stepStatus[step.Name]
		if st == nil {
			continue
		}
		lineIdx++ // 步骤主行
		nodes := m.nodeStatus[step.Name]
		if len(nodes) > 0 {
			hasError := false
			for _, ns := range nodes {
				if ns.Status == "failed" {
					hasError = true
					break
				}
			}
			if !hasError && len(nodes) > 5 {
				lineIdx++ // 聚合行
			} else {
				lineIdx += len(nodes)
			}
		}
	}
	return -1
}

// updateLeftOffsetForStep 当步骤变为 Running 时，滚动左栏使该步骤可见并尽量垂直居中。
func (m *Model) updateLeftOffsetForStep(stepName string) {
	bodyHeight := m.height - headerHeight - footerHeight
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	idx := m.stepLineIndex(stepName)
	if idx < 0 {
		return
	}
	// 若索引超出当前可见区域，更新 leftOffset 使该步骤尽量垂直居中
	if idx < m.leftOffset || idx >= m.leftOffset+bodyHeight {
		newOffset := idx - bodyHeight/2
		if newOffset < 0 {
			newOffset = 0
		}
		m.leftOffset = newOffset
	}
}

func (m *Model) refreshLogViewport() {
	if len(m.logs) == 0 {
		return
	}
	var b string
	for _, l := range m.logs {
		prefix := "> "
		if l.TargetID != "" {
			prefix = "> " + l.TargetID + ": "
		}
		b += prefix + l.Message + "\n"
	}
	m.logViewport.SetContent(b)
	if m.autoScroll {
		m.logViewport.GotoBottom()
	}
}

// View 渲染
func (m Model) View() string {
	return renderView(m)
}

// PipelineDone 返回用于通知 TUI 流水线已结束的 tea.Msg
func PipelineDone(err error) tea.Msg {
	return pipelineDoneMsg{Err: err}
}

// SetCancelFunc 设置取消函数；Ctrl+C 时调用以触发 Engine context 取消
func (m *Model) SetCancelFunc(c context.CancelFunc) {
	m.cancelFunc = c
}

// SetApprovalChan 设置审批输入 channel；TUI 模式下 manual_approval 等待时，用户按 y/n 会写入此 channel
func (m *Model) SetApprovalChan(ch chan<- string) {
	m.approvalChan = ch
}

// DeploymentErr 返回流水线执行错误，供 main 判断退出码
func (m Model) DeploymentErr() error {
	return m.err
}

// Ensure Model implements tea.Model
var _ tea.Model = (*Model)(nil)
