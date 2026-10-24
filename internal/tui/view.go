package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const jumpToBottomLabel = " Jump to bottom ↓ "

type jumpPillBounds struct {
	left, right, y int
}

// layout 先为底部状态区和输入坞分配高度，再把剩余空间交给消息视口
func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	follow := m.viewport.AtBottom()
	previousWidth, previousHeight := m.editor.Width(), m.editor.Height()
	targetLine := m.editor.Line()
	lineInfo := m.editor.LineInfo()
	targetColumn := lineInfo.StartColumn + lineInfo.ColumnOffset
	m.editor.SetWidth(max(1, m.width))
	m.editor.SetCursor(targetColumn)
	rows := 0
	probe := textarea.New()
	probe.Prompt = "❯ "
	probe.SetPromptFunc(2, func(line int) string {
		if line == 0 {
			return "❯ "
		}
		return "  "
	})
	probe.ShowLineNumbers = false
	probe.SetWidth(max(1, m.width))
	for _, line := range strings.Split(m.editor.Value(), "\n") {
		// 使用编辑器自身的软换行规则，包含行尾光标与按单词换行占用的空间
		probe.SetValue(line)
		rows += probe.LineInfo().Height
	}
	limit := min(max(5, m.height*3/10), max(1, m.height-4))
	m.editor.SetHeight(max(1, min(rows, limit)))
	if previousWidth != m.editor.Width() || previousHeight != m.editor.Height() {
		refreshEditorViewport(&m.editor, targetLine, targetColumn)
	}
	// Reserve one row for the combined cwd/model status bar in the bottom status area.
	dock := 1 + m.pendingRows() + m.editor.Height() + 2 + m.subagentRows()
	if m.selector != nil {
		m.selector.input.Width = max(1, m.width-3)
		dock = 1 + m.selector.height(max(1, m.height-2)) + m.subagentRows()
	} else {
		dock += m.completionRows(len(m.completionItems()))
	}
	m.viewport.Width = m.width
	m.viewport.Height = max(0, m.height-dock)
	if follow {
		m.viewport.GotoBottom()
	}
}

func refreshEditorViewport(editor *textarea.Model, targetLine, targetColumn int) {
	// textarea 在 SetHeight/SetWidth 后会保留私有视口偏移，先回到输入起点，
	// 再走到原来的逻辑行，使光标停在用户离开的位置
	*editor, _ = editor.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	for steps := 0; steps < 100000; steps++ {
		line := editor.Line()
		if line >= targetLine {
			break
		}
		var next tea.Cmd
		*editor, next = editor.Update(tea.KeyMsg{Type: tea.KeyDown})
		_ = next
	}
	editor.SetCursor(targetColumn)
	// 空 rune 消息只触发 textarea 的 repositionView，不改变已恢复的值与光标列
	*editor, _ = editor.Update(tea.KeyMsg{Type: tea.KeyRunes})
}

// View 只拼接显示缓存，不调用后台运行器或 Markdown 解析器
func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	lines := m.frameLines()
	if m.hasSelection() {
		paintSelection(lines, m.selectionAnchor, m.selectionFocus)
	}
	if len(lines) < m.height {
		lines = append(lines, make([]string, m.height-len(lines))...)
	}
	if len(lines) > m.height {
		// 输入坞高度已预先测量，只有异常子组件才会溢出，此时保住固定底栏边界
		if m.height <= 1 {
			lines = lines[len(lines)-1:]
		} else {
			footerStart := max(0, len(lines)-1)
			lines = append(lines[:min(m.height-1, footerStart)], lines[footerStart:]...)
		}
	}
	m.paintJumpPill(lines)
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	m.updateCursorPosition()
	return strings.Join(lines, "\n")
}

// jumpPillBounds 返回主 transcript 离底时覆盖的 pill 区域
func (m *Model) jumpPillBounds() (jumpPillBounds, bool) {
	if m.closing || m.selector != nil || m.viewport.Height <= 0 || m.viewport.AtBottom() {
		return jumpPillBounds{}, false
	}
	if rows := m.completionRows(len(m.completionItems())); rows > 0 {
		return jumpPillBounds{}, false
	}
	width := ansi.StringWidth(jumpToBottomLabel)
	if width > m.width {
		return jumpPillBounds{}, false
	}
	left := (m.width - width) / 2
	// The transcript starts at the first frame row; overlay its last visible row.
	return jumpPillBounds{left: left, right: left + width, y: m.viewport.Height - 1}, true
}

func (m *Model) jumpPillVisible() bool {
	_, ok := m.jumpPillBounds()
	return ok
}

func (m *Model) jumpPillHovered() bool {
	bounds, ok := m.jumpPillBounds()
	return ok && m.mousePositionSet && m.mousePosition.y == bounds.y && m.mousePosition.x >= bounds.left && m.mousePosition.x < bounds.right
}

// paintJumpPill 在 transcript 最后一行覆盖到底提示
func (m *Model) paintJumpPill(lines []string) {
	bounds, ok := m.jumpPillBounds()
	if !ok || bounds.y < 0 || bounds.y >= len(lines) {
		return
	}
	line := lines[bounds.y]
	lineWidth := ansi.StringWidth(line)
	if lineWidth < m.width {
		line += strings.Repeat(" ", m.width-lineWidth)
	}
	background := userBg
	if m.jumpPillHovered() {
		background = userBgHover
	}
	pill := lipgloss.NewStyle().
		Background(lipgloss.Color(background)).
		Foreground(lipgloss.Color(textColor)).
		Faint(true).
		Render(jumpToBottomLabel)
	lines[bounds.y] = ansi.Cut(line, 0, bounds.left) + pill + ansi.Cut(line, bounds.right, m.width)
}

// jumpToBottom 恢复实时视图并清除可能残留的文本选择
func (m *Model) jumpToBottom() {
	m.viewport.GotoBottom()
	m.clearSelection()
}

func (m *Model) frameLines() []string {
	lines := make([]string, 0, m.height)
	if m.viewport.Height > 0 {
		lines = append(lines, splitFrameLines(m.viewport.View())...)
	}
	if m.selector != nil {
		lines = append(lines, splitFrameLines(m.selector.view(m.width, max(1, m.height-2)))...)
	} else {
		if m.viewingTaskID == "" {
			lines = append(lines, m.pendingLines()...)
		}
		label := ""
		if m.busy {
			label = m.spinner.View() + " " + m.status
		} else if m.status != "" {
			label = m.status
		}
		borderColor := inputBorder
		if m.viewingTaskID != "" {
			label = m.subagentDetailHeader()
			borderColor = blueColor
		}
		border := rule(m.width, label, borderColor, false)
		if m.copyNotice {
			// 复制提示贴在输入框右上边框，不占用新行或覆盖运行状态
			const notice = " copied! ──"
			if m.width >= ansi.StringWidth(label)+ansi.StringWidth(notice)+5 {
				border = rule(m.width-ansi.StringWidth(notice), label, inputBorder, false) + colored(mutedColor, notice)
			}
		}
		lines = append(lines, border)
		lines = append(lines, splitFrameLines(m.editor.View())...)
		lines = append(lines, rule(m.width, "", inputBorder, false))
		if completions := m.completionView(); completions != "" {
			lines = append(lines, splitFrameLines(completions)...)
		}
	}
	// Keep cwd/branch and model/thinking on the same row at the top of the bottom status area.
	lines = append(lines, m.statusBar())
	if m.taskSelector {
		lines = append(lines, colored(dimColor, "↑/↓ to select"))
	}
	lines = append(lines, m.taskRows()...)
	return lines
}

// pendingRows 返回队列区域占用的行数
func (m *Model) pendingRows() int {
	if m.viewingTaskID != "" || m.selector != nil || len(m.state.Steering) == 0 {
		return 0
	}
	rows := len(m.state.Steering) + 2
	available := max(0, m.height-(m.editor.Height()+2)-m.completionRows(len(m.completionItems()))-1-m.subagentRows())
	return min(rows, available)
}

// pendingLines 显示每条待消费消息和撤回提示
func (m *Model) pendingLines() []string {
	if len(m.state.Steering) == 0 {
		return nil
	}
	lines := []string{""}
	for _, message := range m.state.Steering {
		if index := strings.IndexByte(message, '\n'); index >= 0 {
			message = message[:index]
		}
		lines = append(lines, ansi.Truncate(colored(dimColor, "Steering: "+message), m.width, "…"))
	}
	hint := "↳ " + steeringDequeueKeyLabel() + " to edit all queued messages"
	lines = append(lines, ansi.Truncate(colored(dimColor, hint), m.width, "…"))
	rows := m.pendingRows()
	if len(lines) > rows {
		if rows <= 0 {
			return nil
		}
		if rows == 1 {
			return lines[len(lines)-1:]
		}
		// 保留空行与提示，同时尽量多显示最早的消息
		visible := append([]string(nil), lines[:rows-1]...)
		visible = append(visible, lines[len(lines)-1])
		return visible
	}
	return lines
}

func steeringDequeueKeyLabel() string {
	return steeringDequeueKeyLabelForOS(runtime.GOOS)
}

func steeringDequeueKeyLabelForOS(goos string) string {
	if goos == "darwin" {
		return "option+up"
	}
	return "alt+up"
}

func splitFrameLines(view string) []string {
	view = strings.TrimSuffix(view, "\n")
	if view == "" {
		return nil
	}
	return strings.Split(view, "\n")
}

func rule(width int, label, color string, centered bool) string {
	if label == "" || width < 5 {
		return colored(color, strings.Repeat("─", width))
	}
	label = ansi.Truncate(label, max(1, width-4), "")
	remaining := max(0, width-ansi.StringWidth(label)-2)
	left := min(2, remaining)
	if centered {
		left = remaining / 2
	}
	return colored(color, strings.Repeat("─", left)+" "+label+" "+strings.Repeat("─", remaining-left))
}

func formatElapsed(d time.Duration) string {
	total := max(1, int(d.Round(time.Second).Seconds()))
	hours, total := total/3600, total%3600
	minutes, seconds := total/60, total%60
	if hours > 0 {
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}
	if minutes > 0 {
		if seconds > 0 {
			return fmt.Sprintf("%dm %ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", seconds)
}

// statusBar 在输入框下方同时显示工作目录、分支、模型和 thinking
func (m *Model) statusBar() string {
	left := m.cwdLabel()
	right := m.modelStatus()
	if m.width <= 0 {
		return ""
	}
	left = ansi.Truncate(left, m.width, "…")
	right = ansi.Truncate(right, max(0, m.width-ansi.StringWidth(left)-1), "…")
	if ansi.StringWidth(left)+ansi.StringWidth(right) >= m.width {
		left = truncateCwdLabel(left, max(0, m.width-ansi.StringWidth(right)-1))
		right = ansi.Truncate(right, max(0, m.width-ansi.StringWidth(left)-1), "…")
	}
	gap := max(0, m.width-ansi.StringWidth(left)-ansi.StringWidth(right))
	return colored(dimColor, left) + strings.Repeat(" ", gap) + colored(dimColor, right)
}

func truncateCwdLabel(label string, width int) string {
	if width <= 0 {
		return ""
	}
	branchStart := strings.LastIndex(label, " [")
	if branchStart < 0 || !strings.HasSuffix(label, "]") {
		return ansi.Truncate(label, width, "…")
	}
	branch := label[branchStart:]
	branchWidth := ansi.StringWidth(branch)
	if branchWidth >= width {
		return ansi.Truncate(branch, width, "…")
	}
	return ansi.Truncate(label[:branchStart], width-branchWidth, "…") + branch
}

func (m *Model) cwdLabel() string {
	cwd := m.state.Cwd
	if home, err := os.UserHomeDir(); err == nil {
		if cwd == home {
			cwd = "~"
		} else if strings.HasPrefix(cwd, home+string(filepath.Separator)) {
			cwd = "~" + strings.TrimPrefix(cwd, home)
		}
	}
	if m.state.Branch != "" {
		cwd += " [" + m.state.Branch + "]"
	}
	return cwd
}

func (m *Model) modelStatus() string {
	model := m.currentModel()
	right := model.ID
	if len(model.Levels) > 1 {
		level := m.state.Thinking
		if level == "off" {
			level = "thinking off"
		}
		right += " • " + level
	}
	withProvider := "(" + model.Provider + ") " + right
	if m.width >= 60 && len(m.state.Models) > 1 {
		providers := make(map[string]bool)
		for _, model := range m.state.Models {
			providers[model.Provider] = true
		}
		if len(providers) > 1 && ansi.StringWidth(withProvider) <= m.width {
			right = withProvider
		}
	}
	return right
}
