package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

type choice struct {
	value, label, detail, description string
	parentID                          string
	depth                             int
	current                           bool
}

// selector 在输入坞内展示，不创建覆盖消息区的全屏 overlay
type selector struct {
	kind, title     string
	input           textinput.Model
	items, filtered []choice
	selected        int
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
		s.title = "Thinking Level"
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
	return min(budget, base+min(10, rows))
}

func (s *selector) baseHeight(budget int) int {
	if budget < 20 {
		return 6
	}
	if s.kind == "tree" {
		return 9
	}
	return 12
}

// view 小窗口省略空白与详情，优先保留标题、搜索框和边框
func (s *selector) view(width, budget int) string {
	height := s.height(budget)
	compact := budget < 20
	base := s.baseHeight(budget)
	count := 0
	if height > base {
		count = min(10, min(height-base, len(s.filtered)))
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
	for i := start; i < end; i++ {
		item := s.filtered[i]
		cursor, check := "  ", "  "
		if i == s.selected {
			cursor = "→ "
		}
		if item.current {
			check = "✓ "
		}
		color := textColor
		if i == s.selected {
			color = accentColor
		}
		label := item.label
		// 模型 provider 和 default 标记保持 muted，不给整行统一着色
		if s.kind == "model" {
			if n := strings.LastIndex(label, " ["); n >= 0 {
				label = colored(color, label[:n]) + colored(mutedColor, label[n:])
			}
		} else {
			label = colored(color, label)
		}
		line := colored(accentColor, cursor+check) + label
		if item.description != "" {
			line += "  " + colored(mutedColor, item.description)
		}
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	if len(s.filtered) == 0 && height > base {
		lines = append(lines, colored(mutedColor, "  No matches"))
	}
	position := ""
	if len(s.filtered) > 0 {
		position = fmt.Sprintf("  (%d/%d)", s.selected+1, len(s.filtered))
	}
	lines = append(lines, colored(mutedColor, position))
	if !compact && s.kind != "tree" {
		detail := ""
		if len(s.filtered) > 0 {
			detail = s.filtered[s.selected].detail
		}
		lines = append(lines, "", colored(mutedColor, ansi.Truncate("  "+detail, width, "…")), "")
	}
	lines = append(lines, colored(dimColor, ansi.Truncate("  Enter to select · Escape/Ctrl+C to cancel", width, "…")), rule(width, "", borderColor, false))
	if len(lines) > height {
		// 极小窗口优先保留标题和搜索框，避免从底部裁掉固定 UI
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
		m.selector = nil
		m.layout()
		return tea.Batch(m.editor.Focus(), m.dispatch(s.kind, item.value))
	default:
		return m.updateSelectorInput(msg)
	}
	return nil
}

// updateSelectorInput forwards non-tree selector keys to the search input.
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

// moveTreeSelection skips descendants and moves to the next visible user at
// the same depth, keeping one arrow press within the current tree level.
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
		// The last sibling continues into its first child when no sibling remains.
		s.moveTreeToFirstChild()
		return
	}
	if current.depth == 0 {
		// A root has no parent, so moving upward wraps to its last second-level child.
		s.moveTreeToLastChild()
		return
	}
	// The first non-root sibling moves back to its visible parent.
	s.moveTreeToParent()
}

// moveTreeToParent returns to the visible user parent of the selected node.
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

// moveTreeToFirstChild enters the first visible child of the selected node.
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

// moveTreeToLastChild enters the last visible child of the selected node.
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
