package tui

import (
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const timeDoublePress = 500 * time.Millisecond

type slashCommand struct{ name, description string }

var commands = []slashCommand{
	{"tree", "Select a user message to rewind the conversation"},
	{"model", "<provider/model> — Select model (opens selector UI)"},
	{"thinking", "<level> — Set thinking level"},
	{"resume", "Resume a previous session"},
	{"rename", "<new Name> — Rename the current session"},
	{"new", "Start a new session"},
	{"reload", "Reload context files and skills"},
	{"clear", "Clear the current conversation context"},
	{"delete", "Delete the current session and exit Rivo"},
	{"compact", "[instructions] — Compact the conversation"},
	{"exit", "Exit Rivo"},
}

func (m *Model) completionItems() []slashCommand {
	value := strings.TrimLeftFunc(m.editor.Value(), unicode.IsSpace)
	if m.completionHidden || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \n\t\r") {
		return nil
	}
	items := append([]slashCommand(nil), commands...)
	for _, skill := range m.state.Skills {
		items = append(items, slashCommand{name: "skill:" + skill.Name, description: skill.Description})
	}
	// 裸名匹配优先，再补充只匹配完整 skill: 名称的结果
	matched := fuzzyCommands(items, value[1:], true)
	seen := make(map[string]bool, len(matched))
	for _, item := range matched {
		seen[item.name] = true
	}
	var remaining []slashCommand
	for _, item := range items {
		if strings.HasPrefix(item.name, "skill:") && !seen[item.name] {
			remaining = append(remaining, item)
		}
	}
	return append(matched, fuzzyCommands(remaining, value[1:], false)...)
}

// completionRows 只限制显示窗口，完整候选仍可通过上下键选取
func (m *Model) completionRows(count int) int {
	room := max(0, m.height-2-2-m.editor.Height()-1)
	return min(count, min(5, room))
}

func (m *Model) completionView() string {
	items := m.completionItems()
	rows := m.completionRows(len(items))
	if rows == 0 {
		return ""
	}
	start := max(0, min(m.completion, len(items)-1)-rows+1)
	var lines []string
	for i := start; i < min(len(items), start+rows); i++ {
		command := items[i]
		prefix, color := "  ", textColor
		if i == m.completion {
			prefix, color = "→ ", accentColor
		}
		primary := min(32, max(10, m.width/3))
		name := prefix + command.name
		line := colored(color, name)
		if m.width > primary+12 {
			line += strings.Repeat(" ", max(1, primary-ansi.StringWidth(name))) + colored(mutedColor, command.description)
		}
		lines = append(lines, ansi.Truncate(line, m.width, "…"))
	}
	return strings.Join(lines, "\n")
}

// handleKey 焦点优先级：选择器 → 补全 → 应用动作 → 编辑器
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.closing {
		return nil
	}
	if msg.String() == "esc" && m.jumpPillVisible() {
		m.jumpToBottom()
		return nil
	}
	if m.selector != nil {
		return m.selectorKey(msg)
	}
	// 终端 bracketed paste 整段到达：只有粘贴走折叠路径，手动输入不经过这里
	if msg.Paste {
		m.handlePaste(string(msg.Runes))
		return nil
	}
	items := m.completionItems()
	if m.completionRows(len(items)) > 0 {
		switch msg.String() {
		case "up":
			m.completion = max(0, m.completion-1)
			return nil
		case "down":
			m.completion = min(len(items)-1, m.completion+1)
			return nil
		case "esc":
			m.completionHidden = true
			m.layout()
			return nil
		case "tab", "enter":
			leading := m.editor.Value()[:len(m.editor.Value())-len(strings.TrimLeftFunc(m.editor.Value(), unicode.IsSpace))]
			m.editor.SetValue(leading + "/" + items[min(m.completion, len(items)-1)].name + " ")
			m.editor.CursorEnd()
			m.resetPastes()
			m.completionHidden = true
			m.layout()
			if msg.String() == "tab" {
				return nil
			}
		}
	}
	switch msg.String() {
	case "ctrl+v":
		return m.readClipboard()
	case "ctrl+c":
		if m.hasSelection() {
			return m.copySelection()
		}
		now := m.clock()
		if !m.lastCtrlC.IsZero() && now.Sub(m.lastCtrlC) < timeDoublePress {
			return m.quit()
		}
		m.lastCtrlC = now
		m.editor.Reset()
		m.resetPastes()
		m.completionHidden = true
		m.layout()
		return nil
	case "ctrl+d":
		if m.editor.Value() == "" {
			return m.quit()
		}
	case "esc":
		if m.busy && m.handlers.Cancel != nil {
			m.status = "Cancelling…"
			m.handlers.Cancel()
		}
		return nil
	case "ctrl+l":
		return m.openSelector("model", "")
	case "ctrl+t":
		m.hideThinking = !m.hideThinking
		m.refreshTranscript()
		return nil
	case "ctrl+o":
		m.showAllTools = !m.showAllTools
		m.refreshTranscript()
		return nil
	case "shift+tab":
		levels := m.currentModel().Levels
		if len(levels) == 0 {
			return nil
		}
		index := 0
		for i, level := range levels {
			if level == m.state.Thinking {
				index = (i + 1) % len(levels)
				break
			}
		}
		return m.dispatch("thinking", levels[index])
	case "pgup", "pgdown", "ctrl+home", "ctrl+end":
		if msg.String() == "ctrl+home" {
			m.viewport.GotoTop()
			return nil
		}
		if msg.String() == "ctrl+end" {
			m.viewport.GotoBottom()
			return nil
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return cmd
	case "enter":
		return m.submit()
	case "backspace":
		if m.backspaceAtMarker() {
			m.layout()
			return nil
		}
	case "up":
		if m.editor.Line() == 0 && m.editor.LineInfo().RowOffset == 0 {
			m.browseHistory(-1)
			return nil
		}
	case "down":
		line := m.editor.LineInfo()
		if m.editor.Line() == m.editor.LineCount()-1 && line.RowOffset == line.Height-1 {
			m.browseHistory(1)
			return nil
		}
	}
	before := m.editor.Value()
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	if m.editor.Value() != before {
		m.clearSelection()
		m.completion, m.completionHidden = 0, false
		m.layout()
	}
	return cmd
}

func (m *Model) quit() tea.Cmd {
	m.closing = true
	if m.handlers.Cancel != nil {
		m.handlers.Cancel()
	}
	return tea.Quit
}

func (m *Model) browseHistory(direction int) {
	if len(m.history) == 0 {
		return
	}
	if m.historyPos == len(m.history) {
		m.draft = m.editor.Value()
	}
	m.historyPos = max(0, min(len(m.history), m.historyPos+direction))
	value := m.draft
	if m.historyPos < len(m.history) {
		value = m.history[m.historyPos]
	}
	m.editor.SetValue(value)
	m.editor.CursorEnd()
	m.resetPastes()
	m.completionHidden = true
	m.layout()
}

func (m *Model) submit() tea.Cmd {
	input := strings.TrimSpace(m.expandPasteMarkers(m.editor.Value()))
	if input == "" {
		return nil
	}
	if m.busy && input != "/exit" {
		if input == "/tree" || strings.HasPrefix(input, "/tree ") {
			m.notice("Wait for the current operation to finish before opening the tree.")
		} else {
			m.notice("An operation is running. Press Escape to interrupt.")
		}
		return nil
	}
	m.history = append(m.history, input)
	m.historyPos = len(m.history)
	m.draft = ""
	m.editor.Reset()
	m.resetPastes()
	m.completionHidden = true
	m.layout()
	if !strings.HasPrefix(input, "/") {
		m.viewport.GotoBottom()
		return m.dispatch("run", input)
	}
	name, arg := input, ""
	if index := strings.IndexFunc(input, unicode.IsSpace); index >= 0 {
		name, arg = input[:index], strings.TrimSpace(input[index:])
	}
	if strings.HasPrefix(name, "/skill:") {
		m.viewport.GotoBottom()
		return m.dispatch("run", input)
	}
	switch name {
	case "/exit":
		return m.quit()
	case "/model":
		if arg != "" {
			var matches []ModelOption
			for _, model := range m.state.Models {
				if strings.EqualFold(model.Key(), arg) || strings.EqualFold(model.ID, arg) {
					matches = append(matches, model)
				}
			}
			if len(matches) == 1 {
				return m.dispatch("model", matches[0].Key())
			}
		}
		return m.openSelector("model", arg)
	case "/tree":
		return m.openSelector("tree", arg)
	case "/thinking":
		if arg == "" {
			return m.openSelector("thinking", "")
		}
		for _, level := range m.currentModel().Levels {
			if arg == level {
				return m.dispatch("thinking", level)
			}
		}
		m.notice("Unknown thinking level. Available: " + strings.Join(m.currentModel().Levels, ", "))
	case "/resume":
		return m.openSelector("resume", arg)
	case "/compact":
		return m.dispatch("compact", arg)
	case "/rename":
		if arg == "" {
			m.notice("Usage: /rename <new Name>")
			return nil
		}
		return m.dispatch("rename", arg)
	case "/new", "/reload", "/clear", "/delete":
		if arg != "" {
			m.notice("Usage: " + name)
			return nil
		}
		return m.dispatch(strings.TrimPrefix(name, "/"), "")
	default:
		m.notice("Unknown command: " + name)
	}
	return nil
}
