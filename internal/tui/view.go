package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/x/ansi"
)

// layout 先为 footer 和输入坞分配高度，再把剩余空间交给消息视口
func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	follow := m.viewport.AtBottom()
	m.editor.SetWidth(max(1, m.width-1))
	rows := 0
	probe := textarea.New()
	probe.SetWidth(max(1, m.width-1))
	for _, line := range strings.Split(m.editor.Value(), "\n") {
		// 使用编辑器自身的软换行规则，包含行尾光标和按单词换行占用的空间
		probe.SetValue(line)
		rows += probe.LineInfo().Height
	}
	limit := min(max(5, m.height*3/10), max(1, m.height-5))
	m.editor.SetHeight(max(1, min(rows, limit)))
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

// View 只拼接显示缓存，不调用后台运行器或 Markdown 解析器
func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	var parts []string
	if m.viewport.Height > 0 {
		parts = append(parts, m.viewport.View())
	}
	if m.selector != nil {
		parts = append(parts, m.selector.view(m.width, max(1, m.height-3)))
	} else {
		label := ""
		if m.busy {
			label = m.spinner.View() + " " + m.status
		}
		parts = append(parts, rule(m.width, label, inputBorder, false), m.editor.View(), rule(m.width, "", inputBorder, false))
		if completions := m.completionView(); completions != "" {
			parts = append(parts, completions)
		}
	}
	parts = append(parts, m.footer())
	lines := strings.Split(strings.Join(parts, "\n"), "\n")
	// 极小终端仍遵守总高/列宽约束，保留底部输入及 footer
	if len(lines) > m.height {
		lines = lines[len(lines)-m.height:]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	return strings.Join(lines, "\n")
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

func formatElapsed(d time.Duration) string { return fmt.Sprintf("%ds", max(1, int(d.Seconds()))) }

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
