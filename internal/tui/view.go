package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

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
	limit := min(max(5, m.height*3/10), max(1, m.height-5))
	m.editor.SetHeight(max(1, min(rows, limit)))
	if previousWidth != m.editor.Width() || previousHeight != m.editor.Height() {
		refreshEditorViewport(&m.editor, targetLine, targetColumn)
	}
	dock := m.editor.Height() + 2
	if m.selector != nil {
		m.selector.input.Width = max(1, m.width-3)
		dock = m.selector.height(max(1, m.height-3))
	} else {
		dock += len(m.completionItems())
	}
	m.viewport.Width = m.width
	m.viewport.Height = max(0, m.height-2-dock)
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
			footerStart := max(0, len(lines)-2)
			lines = append(lines[:min(m.height-2, footerStart)], lines[footerStart:]...)
		}
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	m.updateCursorPosition()
	return strings.Join(lines, "\n")
}

// frameLines builds the terminal rows in their stable top-to-bottom order.
func (m *Model) frameLines() []string {
	var lines []string
	if m.viewport.Height > 0 {
		lines = append(lines, splitFrameLines(m.viewport.View())...)
	}
	if m.selector != nil {
		lines = append(lines, splitFrameLines(m.selector.view(m.width, max(1, m.height-3)))...)
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

func formatTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1000000:
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	case n < 10000000:
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	default:
		return fmt.Sprintf("%.0fM", float64(n)/1000000)
	}
}

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
		cwd += " (" + m.state.Branch + ")"
	}
	var stats []string
	if m.state.InputTokens > 0 {
		stats = append(stats, "↑"+formatTokens(m.state.InputTokens))
	}
	if m.state.OutputTokens > 0 {
		stats = append(stats, "↓"+formatTokens(m.state.OutputTokens))
	}
	model := m.currentModel()
	context := "?/?"
	percent := 0.0
	if model.ContextWindow > 0 {
		context = "?/" + formatTokens(model.ContextWindow)
		if m.state.ContextKnown {
			percent = float64(m.state.ContextTokens) * 100 / float64(model.ContextWindow)
			context = fmt.Sprintf("%.1f%%/%s", percent, formatTokens(model.ContextWindow))
		}
	}
	if m.state.AutoCompact {
		context += " (auto)"
	}
	color := dimColor
	if percent > 90 {
		color = redColor
	} else if percent > 70 {
		color = yellowColor
	}
	stats = append(stats, colored(color, context))
	left := ansi.Truncate(colored(dimColor, strings.Join(stats, " ")), m.width, "")
	right := model.ID
	if len(model.Levels) > 1 {
		level := m.state.Thinking
		if level == "off" {
			level = "thinking off"
		}
		right += " • " + level
	}
	providers := make(map[string]bool)
	for _, model := range m.state.Models {
		providers[model.Provider] = true
	}
	withProvider := "(" + model.Provider + ") " + right
	available := m.width - ansi.StringWidth(left) - 2
	if len(providers) > 1 && ansi.StringWidth(withProvider) <= available {
		right = withProvider
	}
	right = ansi.Truncate(right, max(0, available), "")
	gap := strings.Repeat(" ", max(0, m.width-ansi.StringWidth(left)-ansi.StringWidth(right)))
	return colored(dimColor, ansi.Truncate(cwd, m.width, "…")) + "\n" + left + gap + colored(dimColor, right)
}