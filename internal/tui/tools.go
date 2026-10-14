package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	compactToolLimit  = 3
	expandedToolLines = 10
)

var compactSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func cloneToolGroup(group *ToolGroupView) *ToolGroupView {
	if group == nil {
		return nil
	}
	cloned := *group
	cloned.Calls = append([]ToolCallView(nil), group.Calls...)
	cloned.Thinking = append([]ThinkingBlockView(nil), group.Thinking...)
	return &cloned
}

func shortenToolPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func oneLineTool(value any, limit int) string {
	if value == nil {
		return "…"
	}
	text := strings.Join(strings.Fields(fmt.Sprint(value)), " ")
	if text == "" {
		text = "…"
	}
	if limit > 1 && ansi.StringWidth(text) > limit {
		return ansi.Truncate(text, limit, "…")
	}
	return text
}

func toolSummary(call ToolCallView) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return oneLineTool(call.Arguments, 60)
	}
	text := func(value any) string { return oneLineTool(value, 60) }
	path := func(value any) string { return shortenToolPath(text(value)) }
	switch call.Name {
	case "bash", "powershell":
		return text(args["command"])
	case "read", "write", "edit":
		return path(args["path"])
	case "find", "grep":
		return text(args["pattern"]) + " in " + path(args["path"])
	case "ls":
		if args["path"] == nil {
			return "."
		}
		return path(args["path"])
	case "web_search":
		return text(args["query"])
	case "subagent":
		if args["agent"] != nil {
			return text(args["agent"])
		}
		return text(args["task"])
	default:
		for _, key := range []string{"path", "query", "name", "description", "url"} {
			if value := args[key]; value != nil {
				return text(value)
			}
		}
		return "…"
	}
}

func toolIcon(call ToolCallView, frame string) (string, string) {
	switch call.Status {
	case ToolPending:
		return frame, accentColor
	case ToolError, ToolInterrupted:
		return "✗", redColor
	default:
		return "✓", greenColor
	}
}

func toolElapsed(call ToolCallView, now time.Time) string {
	elapsed := call.Elapsed
	if call.Status == ToolPending && !call.StartedAt.IsZero() {
		elapsed = now.Sub(call.StartedAt)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return fmt.Sprintf("(%.1fs)", elapsed.Seconds())
}

func thinkingText(group *ToolGroupView) string {
	if group == nil {
		return ""
	}
	parts := make([]string, 0, len(group.Thinking))
	for _, block := range group.Thinking {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func thinkingTokens(group *ToolGroupView) (int, bool) {
	if group == nil {
		return 0, false
	}
	total := 0
	exact := true
	for _, block := range group.Thinking {
		total += block.Tokens
		exact = exact && block.TokensExact
	}
	if total == 0 {
		exact = false
	}
	return total, exact
}

func formatThinkingTokens(group *ToolGroupView) string {
	tokens, exact := thinkingTokens(group)
	prefix := "≈"
	if exact {
		prefix = ""
	}
	if tokens <= 0 {
		return prefix + "0.0K tok"
	}
	if tokens < 100 {
		return prefix + "<0.1K tok"
	}
	value := float64(tokens) / 1000
	if value < 100 {
		return prefix + fmt.Sprintf("%.1fK tok", value)
	}
	return prefix + fmt.Sprintf("%.0fK tok", value)
}

func previewLines(value string, width, limit int) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	wrapped := ansi.Hardwrap(value, max(1, width), true)
	lines := strings.Split(wrapped, "\n")
	if len(lines) <= limit {
		return lines
	}
	lines = append(lines[:limit], "…")
	return lines
}

func renderToolLine(call ToolCallView, rail string, width int, frame string, now time.Time) string {
	icon, color := toolIcon(call, frame)
	line := rail + colored(color, icon) + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(textColor)).Bold(true).Render(call.Name)
	line += " " + colored(dimColor, toolSummary(call))
	if elapsed := toolElapsed(call, now); elapsed != "" {
		line += " " + colored(mutedColor, elapsed)
	}
	return ansi.Truncate(line, max(1, width), "…")
}

func groupState(group *ToolGroupView) (string, string, bool) {
	if group == nil {
		return "", "", false
	}
	pending := false
	thinkingActive := false
	for _, call := range group.Calls {
		pending = pending || call.Status == ToolPending
	}
	for _, block := range group.Thinking {
		thinkingActive = thinkingActive || block.Active
	}
	openEmpty := !group.Sealed && len(group.Calls) == 0
	working := pending || thinkingActive || openEmpty
	if pending {
		return "tool calling...", accentColor, working
	}
	if thinkingActive || openEmpty {
		return "thinking...", thinkingColor, working
	}
	return "tools done", greenColor, working
}

func markdownPreviewRows(render func(string, string, int) string, value, role string, width, limit int) []string {
	if render == nil {
		return previewLines(value, width, limit)
	}
	text := strings.Trim(render(value, role, width), "\n")
	if text == "" {
		return nil
	}
	rows := strings.Split(text, "\n")
	if len(rows) <= limit {
		return rows
	}
	return append(rows[:limit], "…")
}

func renderToolGroup(group *ToolGroupView, width int, expanded bool, now time.Time) string {
	return renderToolGroupWithMarkdown(group, width, expanded, now, nil)
}

func renderToolGroupWithMarkdown(group *ToolGroupView, width int, expanded bool, now time.Time, render func(string, string, int) string) string {
	return renderToolGroupWithMarkdownFrame(group, width, expanded, now, "", render)
}

func renderToolGroupWithMarkdownFrame(group *ToolGroupView, width int, expanded bool, now time.Time, frame string, render func(string, string, int) string) string {
	if group == nil {
		return ""
	}
	if !group.Active && group.Sealed && len(group.Calls) == 0 && strings.TrimSpace(thinkingText(group)) == "" {
		return ""
	}
	width = max(1, width)
	label, color, working := groupState(group)
	icon := "✓"
	if working {
		icon = frame
		if icon == "" {
			icon = compactSpinner[int(now.UnixMilli()/100)%len(compactSpinner)]
		}
	}
	lines := []string{colored(color, icon+" "+label)}

	if expanded {
		for index, call := range group.Calls {
			rail := "├─ "
			if index == len(group.Calls)-1 && thinkingText(group) == "" {
				rail = "└─ "
			}
			lines = append(lines, renderToolLine(call, rail, width, icon, now))
			childRail := "│   "
			if index == len(group.Calls)-1 {
				childRail = "    "
			}
			for _, row := range markdownPreviewRows(render, call.Result, "tool", max(1, width-4), expandedToolLines) {
				lines = append(lines, colored(dimColor, childRail)+row)
			}
		}
		if text := thinkingText(group); text != "" {
			lines = append(lines, colored(dimColor, "└  ")+colored(mutedColor, "· thinking · "+formatThinkingTokens(group)))
			for _, row := range markdownPreviewRows(render, text, "thinking", max(1, width-4), int(^uint(0)>>1)) {
				lines = append(lines, colored(dimColor, "    ")+colored(thinkingColor, row))
			}
		}
		return strings.Join(lines, "\n")
	}

	shown := len(group.Calls)
	if shown > compactToolLimit {
		shown = compactToolLimit
	}
	reserveThinking := thinkingText(group) != ""
	for offset := 0; offset < shown; offset++ {
		index := len(group.Calls) - 1 - offset
		rail := "│  "
		if offset == shown-1 && !reserveThinking {
			rail = "└  "
		}
		lines = append(lines, renderToolLine(group.Calls[index], rail, width, icon, now))
	}
	if len(group.Calls) > shown {
		lines = append(lines, colored(dimColor, "│  ")+colored(mutedColor, fmt.Sprintf("… +%d more", len(group.Calls)-shown)))
	}
	if reserveThinking {
		text := strings.Join(strings.Fields(thinkingText(group)), " ")
		lines = append(lines, colored(dimColor, "└  ")+colored(mutedColor, "· ")+colored(thinkingColor, "thinking: "+oneLineTool(text, 50))+" "+colored(mutedColor, "· "+formatThinkingTokens(group)))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderToolGroup(group *ToolGroupView, width int) string {
	if group == nil {
		return ""
	}
	if m.hideThinking {
		group = cloneToolGroup(group)
		group.Thinking = nil
	}
	now := time.Now()
	if m.clock != nil {
		now = m.clock()
	}
	frame := strings.TrimSpace(m.spinner.View())
	render := func(value, role string, renderWidth int) string {
		key := role + "\x00" + value + "\x00" + fmt.Sprint(renderWidth)
		if cached, ok := m.markdownPreview[key]; ok {
			return cached
		}
		result := m.markdown.render(value, role, renderWidth)
		m.markdownPreview[key] = result
		if len(m.markdownPreview) > 512 {
			m.markdownPreview = make(map[string]string)
		}
		return result
	}
	return renderToolGroupWithMarkdownFrame(group, width, m.showAllTools, now, frame, render)
}
