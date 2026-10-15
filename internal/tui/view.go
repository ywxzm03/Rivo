package tui

import (
	"fmt"
	"os"
	"path/filepath"
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
		// 使用编辑器自身的软换行规则，包含行尾光标和按单词换行占用的空间
		probe.SetValue(line)
		rows += probe.LineInfo().Height
	}
	limit := min(max(5, m.height*3/10), max(1, m.height-4))
	m.editor.SetHeight(max(1, min(rows, limit)))
	if previousWidth != m.editor.Width() || previousHeight != m.editor.Height() {
		refreshEditorViewport(&m.editor, targetLine, targetColumn)
	}
	dock := m.editor.Height() + 2
	if m.selector != nil {
		m.selector.input.Width = max(1, m.width-3)
		dock = m.selector.height(max(1, m.height-2))
	} else {
		dock += m.completionRows(len(m.completionItems()))
	}
	m.viewport.Width = m.width
	m.viewport.Height = max(0, m.height-1-dock)
	if follow {
		m.viewport.GotoBottom()
	}
}

// refreshEditorViewport repositions the textarea after its dock height changes.
func refreshEditorViewport(editor *textarea.Model, targetLine, targetColumn int) {
	// Bubble's textarea keeps its private viewport offset when SetHeight or
	// SetWidth changes. Re-anchor at the input start, then walk back to the
	// original logical line so the cursor remains where the user left it.
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
	// An empty rune message runs textarea's repositionView without changing
	// the restored value or cursor column.
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
		// The dock is measured before rendering; only a malformed child may
		// overflow. Preserve the fixed footer/input boundary in that case.
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

// paintJumpPill 在 transcript 最后一行覆盖 Claude 风格的到底提示
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
	pill := lipgloss.NewStyle().
		Background(lipgloss.Color(userBg)).
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

// frameLines builds the terminal rows in their stable top-to-bottom order.
func (m *Model) frameLines() []string {
	var lines []string
	if m.viewport.Height > 0 {
		lines = append(lines, splitFrameLines(m.viewport.View())...)
	}
	if m.selector != nil {
		lines = append(lines, splitFrameLines(m.selector.view(m.width, max(1, m.height-2)))...)
	} else {
		label := ""
		if m.busy {
			label = m.spinner.View() + " " + m.status
		} else if m.status != "" {
			label = m.status
		}
		border := rule(m.width, label, inputBorder, false)
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
	lines = append(lines, splitFrameLines(m.footer())...)
	return lines
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
		cwd += " [" + m.state.Branch + "]"
	}
	left := colored(dimColor, ansi.Truncate(cwd, m.width, "…"))
	model := m.currentModel()
	right := model.ID
	if len(model.Levels) > 1 {
		level := m.state.Thinking
		if level == "off" {
			level = "thinking off"
		}
		right += " • " + level
	}
	available := m.width - ansi.StringWidth(ansi.Strip(left))
	withProvider := "(" + model.Provider + ") " + right
	if len(m.state.Models) > 1 {
		providers := make(map[string]bool)
		for _, model := range m.state.Models {
			providers[model.Provider] = true
		}
		if len(providers) > 1 && ansi.StringWidth(withProvider) <= available {
			right = withProvider
		}
	}
	right = colored(dimColor, ansi.Truncate(right, max(0, available), "…"))
	gap := strings.Repeat(" ", max(0, m.width-ansi.StringWidth(ansi.Strip(left))-ansi.StringWidth(ansi.Strip(right))))
	return left + gap + right
}
