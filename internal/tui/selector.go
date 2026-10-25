package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"

	"github.com/ywxzm03/Rivo/internal/filehistory"
)

type choice struct {
	value, label, detail, description string
	parentID                          string
	depth                             int
	current                           bool
}

// selector 在输入坞内展示，不创建覆盖消息区的全屏 overlay
type selector struct {
	kind, title       string
	treeRestoreTarget string
	input             textinput.Model
	items, filtered   []choice
	selected          int
}

func (m *Model) openSelector(kind, query string) tea.Cmd {
	if m.busy {
		if kind == "tree" {
			m.notice("Wait for the current operation to finish before opening the tree.")
		} else {
			m.notice("An operation is running. Press Escape to interrupt.")
		}
		return nil
	}
	s := &selector{kind: kind}
	switch kind {
	case "model":
		s.title = "Select Model"
		models := append([]ModelOption(nil), m.state.Models...)
		sort.SliceStable(models, func(i, j int) bool {
			rank := func(option ModelOption) int {
				if option.Key() == m.state.ModelKey {
					return 0
				}
				if option.Key() == m.state.DefaultModelKey {
					return 1
				}
				return 2
			}
			if rank(models[i]) != rank(models[j]) {
				return rank(models[i]) < rank(models[j])
			}
			return models[i].Key() < models[j].Key()
		})
		for _, model := range models {
			label := model.ID + " [" + model.Provider + "]"
			if model.Key() == m.state.DefaultModelKey {
				label += " · default"
			}
			s.items = append(s.items, choice{value: model.Key(), label: label, detail: "Model Name: " + model.Name, current: model.Key() == m.state.ModelKey})
		}
	case "thinking":
		s.title = "Reasoning Effort"
		for _, level := range m.currentModel().Levels {
			s.items = append(s.items, choice{value: level, label: level, description: thinkingDescription(level), current: level == m.state.Thinking})
		}
	case "resume":
		s.title = "Resume session"
		for _, session := range m.state.Sessions {
			s.items = append(s.items, choice{value: session.Path, label: session.Summary, description: session.Modified, detail: session.Path})
		}
	case "tree":
		s.title = "Conversation tree"
		for _, node := range m.state.Tree {
			s.items = append(s.items, choice{value: node.ID, label: node.Prefix + node.Text, detail: node.Text, parentID: node.ParentID, depth: node.Depth, current: node.Current})
		}
	}
	s.input = textinput.New()
	s.input.Prompt = "> "
	s.input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(textColor))
	s.input.PromptStyle = s.input.TextStyle
	s.input.SetValue(query)
	s.filter()
	if query == "" {
		for i, item := range s.filtered {
			if item.current {
				s.selected = i
				break
			}
		}
	}
	m.selector = s
	m.editor.Blur()
	m.layout()
	return s.input.Focus()
}

func (m *Model) openTreeRestoreSelector(target string) tea.Cmd {
	s := &selector{kind: "tree_restore", title: "Restore conversation tree", treeRestoreTarget: target}
	s.items = []choice{
		{value: string(filehistory.RestoreContext), label: "Restore conversation", detail: "Move the conversation tree only"},
		{value: string(filehistory.RestoreCode), label: "Restore code", detail: "Restore tracked Write/Edit changes only"},
		{value: string(filehistory.RestoreCodeAndContext), label: "Restore code and conversation", detail: "Restore tracked code, then move the conversation tree"},
	}
	s.input = textinput.New()
	s.input.Prompt = "> "
	s.input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(textColor))
	s.input.PromptStyle = s.input.TextStyle
	s.filter()
	m.selector = s
	m.editor.Blur()
	m.layout()
	return s.input.Focus()
}

func thinkingDescription(level string) string {
	switch level {
	case "off":
		return "No reasoning"
	case "minimal":
		return "Very brief reasoning"
	case "low":
		return "Light reasoning"
	case "medium":
		return "Moderate reasoning"
	case "high":
		return "Deep reasoning"
	case "xhigh":
		return "Extra-high reasoning"
	case "max":
		return "Maximum reasoning"
	}
	return ""
}

func (s *selector) filter() {
	s.filtered = nil
	query := strings.TrimSpace(s.input.Value())
	if query == "" {
		s.filtered = append(s.filtered, s.items...)
	} else {
		labels := make([]string, len(s.items))
		for i, item := range s.items {
			labels[i] = item.label + " " + item.description + " " + item.detail
		}
		for _, match := range fuzzy.Find(query, labels) {
			s.filtered = append(s.filtered, s.items[match.Index])
		}
	}
	s.selected = 0
}

func (s *selector) height(budget int) int {
	budget = max(1, budget)
	base := s.baseHeight(budget)
	rows := len(s.filtered)
	if rows == 0 {
		rows = 1
	}
	rows = min(10, rows)
	total := base + rows
	// 还有候选没显示时，底部要额外占一行 "... +N more"
	if rows < len(s.filtered) {
		total++
	}
	return min(budget, total)
}

// numbered 表示是否给每行加序号：tree 保持原有的缩进树形，不参与编号
func (s *selector) numbered() bool {
	return s.kind != "tree"
}

// rowLine 画一行候选：序号、游标位、正文与当前项勾
// 当前项用绿字加勾；被选中时正文仍为绿色，游标仍是紫色箭头
// 游标位在首行/末行且还有被折叠的候选时改画 ↑ / ↓，提示还有内容
func (s *selector) rowLine(index, start, end int, numbered bool, width int) string {
	item := s.filtered[index]
	cursor := "  "
	switch {
	case index == s.selected:
		cursor = "❯ "
	case index == start && start > 0:
		cursor = "↑ "
	case index == end-1 && end < len(s.filtered):
		cursor = "↓ "
	}
	prefix := ""
	if numbered {
		// 序号左对齐：先把 "N." 整体排好，再按最长编号的位数补右侧空格，
		// 这样 9. 与 12. 的正文列一致，点号也不会和数字分家
		width := len(strconv.Itoa(len(s.filtered)))
		prefix = fmt.Sprintf("%-*s  ", width+1, strconv.Itoa(index+1)+".")
	}
	color := textColor
	switch {
	case item.current:
		color = greenColor
	case index == s.selected:
		color = accentColor
	}
	label := item.label
	// provider 与 default 标记保持 muted，不给整行统一着色
	if s.kind == "model" {
		if n := strings.LastIndex(label, " ["); n >= 0 {
			label = colored(color, label[:n]) + colored(mutedColor, label[n:])
		}
	} else {
		label = colored(color, label)
	}
	line := colored(dimColor, prefix) + colored(accentColor, cursor) + label
	if item.description != "" {
		line += "  " + colored(mutedColor, item.description)
	}
	if item.current {
		line += colored(greenColor, " ✓")
	}
	return line
}

// overflowLines 在列表底部提示还有多少候选没显示，上方的剩余量由首行的 ↑ 表达
func (s *selector) overflowLines(width, end int) []string {
	if end >= len(s.filtered) {
		return nil
	}
	noun := "items"
	switch s.kind {
	case "model":
		noun = "models"
	case "resume":
		noun = "sessions"
	case "thinking":
		noun = "levels"
	}
	return []string{colored(mutedColor, fmt.Sprintf("  ... +%d %s", len(s.filtered)-end, noun))}
}

func (s *selector) baseHeight(budget int) int {
	if budget < 20 {
		// 紧凑模式只剩边框、标题、输入框、提示行与底部边框
		return 5
	}
	// 固定区：上下边框、标题、输入框与四处空行、提示行；位置与详情行都不再占位
	return 9
}

func (s *selector) view(width, budget int) string {
	height := s.height(budget)
	compact := budget < 20
	base := s.baseHeight(budget)
	room := max(0, height-base)
	count := min(len(s.filtered), min(10, room))
	// 窗口装不下全部候选时，留一行给底部的 "... +N more"
	if count < len(s.filtered) && count > 0 && count == room {
		count--
	}
	start, end := 0, 0
	if count > 0 {
		start = max(0, min(s.selected-count/2, len(s.filtered)-count))
		end = min(len(s.filtered), start+count)
	}
	lines := []string{rule(width, "", borderColor, false)}
	if !compact {
		lines = append(lines, "")
	}
	lines = append(lines, colored(accentColor, ansi.Truncate(s.title, width, "…")))
	if !compact {
		lines = append(lines, "")
	}
	lines = append(lines, s.input.View())
	if !compact {
		lines = append(lines, "")
	}
	numbered := s.numbered()
	for i := start; i < end; i++ {
		lines = append(lines, ansi.Truncate(s.rowLine(i, start, end, numbered, width), width, "…"))
	}
	if len(s.filtered) == 0 && height > base {
		lines = append(lines, colored(mutedColor, "  No matches"))
	}
	lines = append(lines, s.overflowLines(width, end)...)
	if !compact {
		lines = append(lines, "")
	}
	lines = append(lines, colored(dimColor, ansi.Truncate("  Enter to select · Escape/Ctrl+C to cancel", width, "…")), rule(width, "", borderColor, false))
	if len(lines) > height {
		// 极小窗口优先保留标题和搜索框
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func (m *Model) selectorKey(msg tea.KeyMsg) tea.Cmd {
	s := m.selector
	switch msg.String() {
	case "esc", "ctrl+c":
		m.selector = nil
		m.lastCtrlC = time.Time{}
		m.layout()
		return m.editor.Focus()
	case "up", "ctrl+p":
		if s.kind == "tree" {
			s.moveTreeSelection(-1)
		} else {
			s.selected = max(0, s.selected-1)
		}
	case "down", "ctrl+n":
		if s.kind == "tree" {
			s.moveTreeSelection(1)
		} else {
			s.selected = min(max(0, len(s.filtered)-1), s.selected+1)
		}
	case "left":
		if s.kind == "tree" {
			s.moveTreeToParent()
			return nil
		}
		return m.updateSelectorInput(msg)
	case "right":
		if s.kind == "tree" {
			s.moveTreeToFirstChild()
			return nil
		}
		return m.updateSelectorInput(msg)
	case "pgup":
		s.selected = max(0, s.selected-10)
	case "pgdown":
		s.selected = min(max(0, len(s.filtered)-1), s.selected+10)
	case "enter":
		if len(s.filtered) == 0 {
			return nil
		}
		item := s.filtered[s.selected]
		if s.kind == "tree" {
			return m.openTreeRestoreSelector(item.value)
		}
		m.selector = nil
		m.layout()
		if s.kind == "tree_restore" {
			return tea.Batch(m.editor.Focus(), m.dispatchTreeRestore(s.treeRestoreTarget, filehistory.RestoreMode(item.value)))
		}
		return tea.Batch(m.editor.Focus(), m.dispatch(s.kind, item.value))
	default:
		return m.updateSelectorInput(msg)
	}
	return nil
}

func (m *Model) dispatchTreeRestore(target string, mode filehistory.RestoreMode) tea.Cmd {
	if m.busy {
		m.notice("Wait for the current operation to finish before restoring the tree.")
		return nil
	}
	if m.handlers.Request == nil {
		return nil
	}
	m.busy, m.status = true, "Working"
	m.canceling, m.compacting = false, false
	m.operationID++
	return tea.Batch(m.spinner.Tick, m.handlers.Request(Request{ID: m.operationID, AfterID: m.lastMessageID(), Kind: "tree", Value: target, RestoreMode: mode}))
}

func (m *Model) updateSelectorInput(msg tea.KeyMsg) tea.Cmd {
	s := m.selector
	before := s.input.Value()
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	if before != s.input.Value() {
		s.filter()
		m.layout()
	}
	return cmd
}

// moveTreeSelection 跳过后代，移动到同层下一个可见 user
func (s *selector) moveTreeSelection(direction int) {
	if len(s.filtered) == 0 || direction == 0 {
		return
	}
	current := s.filtered[s.selected]
	for index := s.selected + direction; index >= 0 && index < len(s.filtered); index += direction {
		item := s.filtered[index]
		if item.depth == current.depth && item.parentID == current.parentID {
			s.selected = index
			return
		}
	}
	if direction > 0 {
		// 没有下一个兄弟时进入首个子节点
		s.moveTreeToFirstChild()
		return
	}
	if current.depth == 0 {
		// 根节点无父节点，向上时绕到最后一个二级子节点
		s.moveTreeToLastChild()
		return
	}
	// 非根节点回到可见父节点
	s.moveTreeToParent()
}

func (s *selector) moveTreeToParent() {
	if len(s.filtered) == 0 {
		return
	}
	parentID := s.filtered[s.selected].parentID
	if parentID == "" {
		s.moveTreeToLastChild()
		return
	}
	for index, item := range s.filtered {
		if item.value == parentID {
			s.selected = index
			return
		}
	}
}

func (s *selector) moveTreeToFirstChild() {
	if len(s.filtered) == 0 {
		return
	}
	parentID := s.filtered[s.selected].value
	depth := s.filtered[s.selected].depth
	for index, item := range s.filtered {
		if item.parentID == parentID && item.depth == depth+1 {
			s.selected = index
			return
		}
	}
}

func (s *selector) moveTreeToLastChild() {
	if len(s.filtered) == 0 {
		return
	}
	parentID := s.filtered[s.selected].value
	depth := s.filtered[s.selected].depth
	for index := len(s.filtered) - 1; index >= 0; index-- {
		item := s.filtered[index]
		if item.parentID == parentID && item.depth == depth+1 {
			s.selected = index
			return
		}
	}
}
