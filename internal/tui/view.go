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

// branchIcon 是 git 分支图标（Nerd Font octicon git-branch），放在分支名左侧
const branchIcon = "\uf418"

type jumpPillBounds struct {
	left, right, y int
}

// layout 先为 footer 和输入坞分配高度，再把剩余空间交给消息视口
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
	dock := m.pendingRows() + m.editor.Height() + 2
	if m.selector != nil {
		m.selector.input.Width = max(1, m.width-3)
		dock = m.selector.height(max(1, m.height-2))
	} else {
		// 视口与输入框上边框之间保留一行空白，避免长消息贴边
		dock += m.completionRows(len(m.completionItems())) + 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = max(0, m.height-1-dock)
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
	var lines []string
	if m.viewport.Height > 0 {
		lines = append(lines, splitFrameLines(m.viewport.View())...)
	}
	if m.selector != nil {
		lines = append(lines, splitFrameLines(m.selector.view(m.width, max(1, m.height-2)))...)
	} else {
		lines = append(lines, m.pendingLines()...)
		// 消息区与输入框之间留一行空白，长消息不会贴上边框
		lines = append(lines, "")
		// 候选列表排在输入框上方，向上展开，输入框位置保持不变
		if completions := m.completionView(); completions != "" {
			lines = append(lines, splitFrameLines(completions)...)
		}
		label := ""
		borderColor := inputBorder
		if m.busy {
			// 运行中的旋转器与状态文字用主题紫，与选中态一致
			label = m.spinner.View() + " " + m.status
			borderColor = accentColor
		} else if m.status != "" {
			label = m.status
		}
		border := rule(m.width, label, borderColor, false)
		if m.copyNotice {
			// 复制提示贴在输入框右上边框，不占用新行或覆盖运行状态
			const notice = " copied! ──"
			if m.width >= ansi.StringWidth(label)+ansi.StringWidth(notice)+5 {
				border = rule(m.width-ansi.StringWidth(notice), label, borderColor, false) + colored(mutedColor, notice)
			}
		}
		lines = append(lines, border)
		lines = append(lines, splitFrameLines(m.editor.View())...)
		// 上下两条边框同色，运行中一起变紫
		lines = append(lines, rule(m.width, "", borderColor, false))
	}
	lines = append(lines, splitFrameLines(m.footer())...)
	return lines
}

// pendingRows 返回队列区域占用的行数
func (m *Model) pendingRows() int {
	if m.selector != nil || (len(m.state.Steering) == 0 && len(m.state.FollowUp) == 0) {
		return 0
	}
	rows := len(m.state.Steering) + len(m.state.FollowUp) + 1
	if len(m.state.Steering) > 0 {
		rows++
	}
	if len(m.state.FollowUp) > 0 {
		rows++
	}
	available := max(0, m.height-1-(m.editor.Height()+2)-m.completionRows(len(m.completionItems())))
	return min(rows, available)
}

// pendingLines 显示两类待消费消息和各自的回填提示
func (m *Model) pendingLines() []string {
	if len(m.state.Steering) == 0 && len(m.state.FollowUp) == 0 {
		return nil
	}
	lines := []string{""}
	for _, message := range m.state.Steering {
		if index := strings.IndexByte(message, '\n'); index >= 0 {
			message = message[:index]
		}
		lines = append(lines, ansi.Truncate(colored(dimColor, "Steering: "+message), m.width, "…"))
	}
	for _, message := range m.state.FollowUp {
		if index := strings.IndexByte(message, '\n'); index >= 0 {
			message = message[:index]
		}
		lines = append(lines, ansi.Truncate(colored(dimColor, "Follow-up: "+message), m.width, "…"))
	}
	if len(m.state.Steering) > 0 {
		lines = append(lines, ansi.Truncate(colored(dimColor, "↳ "+steeringDequeueKeyLabel()+" to edit steering"), m.width, "…"))
	}
	if len(m.state.FollowUp) > 0 {
		lines = append(lines, ansi.Truncate(colored(dimColor, "↳ "+followUpDequeueKeyLabel()+" to edit follow-up"), m.width, "…"))
	}
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

func followUpDequeueKeyLabel() string {
	return followUpDequeueKeyLabelForOS(runtime.GOOS)
}

func followUpDequeueKeyLabelForOS(goos string) string {
	if goos == "darwin" {
		return "option+down"
	}
	return "alt+down"
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

// footer 输出单行状态栏：左侧工作目录与分支，右侧模型与思考等级，两端对齐
func (m *Model) footer() string {
	cwd := m.state.Cwd
	if home, err := os.UserHomeDir(); err == nil {
		if cwd == home {
			cwd = "~"
		} else if strings.HasPrefix(cwd, home+string(filepath.Separator)) {
			cwd = "~" + strings.TrimPrefix(cwd, home)
		}
	}
	if m.state.Branch != "" {
		// 分支前用 git 图标，与目录之间用小点分隔
		cwd += " • " + branchIcon + " " + m.state.Branch
	}
	left := colored(dimColor, ansi.Truncate(cwd, m.width, "…"))
	model := m.currentModel()
	right := model.ID
	if len(model.Levels) > 1 {
		level := m.state.Thinking
		if level == "off" {
			level = "effort off"
		}
		right += " • " + level
	}
	available := m.width - ansi.StringWidth(ansi.Strip(left))
	right = colored(dimColor, ansi.Truncate(right, max(0, available), "…"))
	gap := strings.Repeat(" ", max(0, m.width-ansi.StringWidth(ansi.Strip(left))-ansi.StringWidth(ansi.Strip(right))))
	return left + gap + right
}
