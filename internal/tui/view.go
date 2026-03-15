package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")).
			Padding(0, 1)

	subHeaderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(0, 1)

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Padding(0, 0)

	// Step status icons
	iconDone    = "✅"
	iconRunning = "⏳"
	iconWaiting = "⏸"
	iconFailed  = "🔴"
	iconPending = "○"

	stepDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stepRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	stepWaitingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stepFailedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	stepPendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	nodeDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	nodeRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	nodeFailedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	logErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	logWarnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	logInfoStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// renderView 渲染左右分栏布局：Header | LeftPane | RightPane | Footer
func renderView(m Model) string {
	header := renderHeader(m)
	mainBody := renderMainBody(m)
	footer := renderFooter(m)

	parts := []string{header, mainBody, footer}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderMainBody 左右分栏：左栏步骤树 | 右栏日志 Viewport
func renderMainBody(m Model) string {
	bodyHeight := m.height - headerHeight - footerHeight
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	leftPane := renderLeftPane(m, bodyHeight)
	rightPane := renderRightPane(m, bodyHeight)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
}

// renderLeftPane 左栏：Pipeline Steps 步骤树
func renderLeftPane(m Model, bodyHeight int) string {
	lines := buildStepLines(m)
	doneCount := 0
	for _, st := range m.stepStatus {
		if st != nil && st.Status == "done" {
			doneCount++
		}
	}
	totalCount := len(m.order)
	title := fmt.Sprintf("▼ Pipeline Steps (%d/%d)", doneCount, totalCount)
	if totalCount == 0 {
		title = "▼ Pipeline Steps"
	}
	header := subHeaderStyle.Render(title)

	// 使用 leftOffset 截取可见行
	start := m.leftOffset
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		visible := []string{header, subHeaderStyle.Render("(no steps yet)")}
		content := strings.Join(visible, "\n")
		return leftPaneStyle(m.leftPaneWidth).Render(content)
	}
	end := start + bodyHeight - 1
	if end > len(lines) {
		end = len(lines)
	}
	visibleLines := lines[start:end]
	content := strings.Join(append([]string{header}, visibleLines...), "\n")

	return leftPaneStyle(m.leftPaneWidth).Render(content)
}

// leftPaneStyle 左栏样式：宽度 + 右边框
func leftPaneStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(width).
		PaddingRight(1).
		Border(lipgloss.NormalBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderForeground(lipgloss.Color("240"))
}

// renderRightPane 右栏：Real-time Logs 日志区
func renderRightPane(m Model, bodyHeight int) string {
	title := subHeaderStyle.Render("▼ Real-time Logs & Output")
	vpContent := m.logViewport.View()
	content := strings.Join([]string{title, vpContent}, "\n")
	return rightPaneStyle(m.rightPaneWidth).Render(content)
}

// rightPaneStyle 右栏样式
func rightPaneStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(width).
		PaddingLeft(1)
}

// buildStepLines 构建步骤树行列表，供左栏渲染和截取
func buildStepLines(m Model) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var lines []string
	for _, step := range m.order {
		st := m.stepStatus[step.Name]
		if st == nil {
			continue
		}

		icon, style := getStepIconAndStyle(st.Status)

		// 🌟 1. 根据插件类型动态推断更有语义的分类标签
		cat := "[Local]"
		if len(step.Roles) > 0 {
			switch step.Type {
			case "http_check", "systemd_check", "docker_check":
				cat = "[Check]"
			case "nginx_config", "systemd_service":
				cat = "[Config]"
			case "release_pruner":
				cat = "[Prune]"
			case "symlink_switch":
				cat = "[Switch]"
			default:
				cat = "[Remote]"
			}
		}

		// 🌟 2. 格式化耗时
		dur := ""
		if st.Duration > 0 {
			dur = fmt.Sprintf("(%.1fs)", st.Duration.Seconds())
		} else if st.Status == "running" {
			dur = "(Running...)"
		} else if st.Status == "waiting" {
			dur = "(Waiting...)"
		}

		// 🌟 3. 使用严格的 %-8s 和 %-28s 保证纵向对齐
		line := style.Render(fmt.Sprintf("%s %-8s %-28s %s", icon, cat, step.Name, dur))
		lines = append(lines, " "+line)

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
				doneCount := 0
				for _, ns := range nodes {
					if ns.Status == "done" {
						doneCount++
					}
				}
				// 聚合行使用 └─ 闭合
				aggLine := fmt.Sprintf("   └─ 🔵 %s (%d/%d nodes done)", step.Name, doneCount, len(nodes))
				lines = append(lines, subHeaderStyle.Render(aggLine))
			} else {
				// 🌟 4. 将 NodeID 提取并排序，保证 UI 刷新时不跳动
				var nodeIDs []string
				for nodeID := range nodes {
					nodeIDs = append(nodeIDs, nodeID)
				}
				sort.Strings(nodeIDs)

				// 🌟 5. 遍历排序后的节点，精准绘制树形线条（子节点只展示 Running/Done/Failed，不展示日志）
				for i, nodeID := range nodeIDs {
					ns := nodes[nodeID]
					nicon, nstyle := getNodeIconAndStyle(ns.Status)
					msg := nodeStatusLabel(ns.Status)
					if ns.Progress > 0 && ns.Progress < 100 {
						msg = fmt.Sprintf("%s %d%%", msg, ns.Progress)
					}

					// 判断是否为该步骤下的最后一个子节点
					branch := "├─"
					if i == len(nodeIDs)-1 {
						branch = "└─"
					}

					// 节点同样做适当对齐
					nodeLine := fmt.Sprintf("   %s %s %-16s : %s", branch, nicon, nodeID, msg)
					lines = append(lines, nstyle.Render(nodeLine))
				}
			}
		}
	}

	if m.done {
		if m.err != nil {
			lines = append(lines, "", stepFailedStyle.Render("❌ Deployment finished with error: "+m.err.Error()))
		} else {
			msg := "✅ Deployment successfully completed."
			if m.deployVersion != "" {
				msg = fmt.Sprintf("✅ Deployment successfully completed. Version: %s", m.deployVersion)
				if m.deployMessage != "" {
					msg = fmt.Sprintf("✅ Deployment successfully completed. Version: %s | %s", m.deployVersion, m.deployMessage)
				}
			} else if m.deployMessage != "" {
				msg = fmt.Sprintf("✅ Deployment successfully completed. %s", m.deployMessage)
			}
			lines = append(lines, "", stepDoneStyle.Render(msg))
		}
	}

	return lines
}

// renderHeader 顶部：全局流水线状态
func renderHeader(m Model) string {
	env := m.effectiveEnv
	version := "v0.0.0"
	if m.cfg.Variables != nil {
		if v, ok := m.cfg.Variables["version"]; ok {
			version = v
		}
	}
	release := m.cfg.Pipeline.Name
	if release == "" {
		release = "Deploy"
	}
	by := "user"
	if m.infra != nil && m.infra.GlobalVars != nil {
		if b, ok := m.infra.GlobalVars["user"]; ok {
			by = b
		}
	}
	if m.cfg.Variables != nil {
		if b, ok := m.cfg.Variables["user"]; ok {
			by = b
		}
	}

	line1 := headerStyle.Render(fmt.Sprintf("🚀 DeployFlow Orchestrator (Env: %s, Version: %s)", env, version))
	line2 := subHeaderStyle.Render(fmt.Sprintf("📦 Release: %q | 👤 By: %s", release, by))
	divider := dividerStyle.Render(strings.Repeat("-", max(40, m.width-2)))

	return lipgloss.JoinVertical(lipgloss.Left, line1, line2, "", divider)
}

func getStepIconAndStyle(status string) (string, lipgloss.Style) {
	switch status {
	case "done":
		return iconDone, stepDoneStyle
	case "running":
		return iconRunning, stepRunningStyle
	case "waiting":
		return iconWaiting, stepWaitingStyle
	case "failed":
		return iconFailed, stepFailedStyle
	default:
		return iconPending, stepPendingStyle
	}
}

func getNodeIconAndStyle(status string) (string, lipgloss.Style) {
	switch status {
	case "done":
		return "🟢", nodeDoneStyle
	case "running":
		return "🔵", nodeRunningStyle
	case "failed":
		return "🔴", nodeFailedStyle
	default:
		return "⚪", nodeRunningStyle
	}
}

// nodeStatusLabel 子节点只展示 Running/Done/Failed，不展示日志
func nodeStatusLabel(status string) string {
	switch status {
	case "done":
		return "Done"
	case "failed":
		return "Failed"
	case "running":
		return "Running"
	default:
		return "Pending"
	}
}

// renderFooter 底部：单行快捷键提示
func renderFooter(m Model) string {
	autoStr := "OFF"
	if m.autoScroll {
		autoStr = "ON"
	}
	return subHeaderStyle.Render(fmt.Sprintf("  [q] Quit   [ctrl+c] Cancel Deployment  |  Auto-scrolling: %s", autoStr))
}
