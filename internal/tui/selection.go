package tui

import (
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) hasSelection() bool {
	return m.sel.anchor != m.sel.focus
}

func (m *Model) clearSelection() {
	m.sel.anchor = screenPoint{}
	m.sel.focus = screenPoint{}
	m.sel.active = false
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	m.sel.mouse = screenPoint{x: msg.X, y: msg.Y}
	m.sel.mouseSet = true
	if mouseIsWheel(msg) {
		if m.composer.selector == nil {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return cmd
		}
		return nil
	}
	if msg.Button == tea.MouseButtonRight && msg.Action == tea.MouseActionPress {
		return m.readClipboard()
	}
	if m.composer.selector != nil {
		return nil
	}
	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button != tea.MouseButtonLeft {
			return nil
		}
		if bounds, ok := m.jumpPillBounds(); ok && msg.Y == bounds.y && msg.X >= bounds.left && msg.X < bounds.right {
			m.jumpToBottom()
			return nil
		}
		now := time.Now()
		if m.clock != nil {
			now = m.clock()
		}
		if now.Sub(m.sel.lastClickAt) <= 450*time.Millisecond && msg.Y == m.sel.lastClick.y && abs(msg.X-m.sel.lastClick.x) <= 2 {
			if left, right, ok := wordBounds(m.frameLines(), screenPoint{x: msg.X, y: msg.Y}); ok {
				m.sel.anchor = screenPoint{x: left, y: msg.Y}
				m.sel.focus = screenPoint{x: right, y: msg.Y}
				m.sel.active = false
				m.sel.lastClickAt = time.Time{}
				return m.copySelection()
			}
		}
		m.sel.lastClick, m.sel.lastClickAt = screenPoint{x: msg.X, y: msg.Y}, now
		m.sel.anchor = screenPoint{x: msg.X, y: msg.Y}
		m.sel.focus = m.sel.anchor
		m.sel.active = true
	case tea.MouseActionMotion:
		if m.sel.active {
			m.sel.focus = screenPoint{x: msg.X, y: msg.Y}
		}
	case tea.MouseActionRelease:
		if m.sel.active {
			m.sel.focus = screenPoint{x: msg.X, y: msg.Y}
			m.sel.active = false
			// 没有拖动就是一次单击：落在工具行、分组摘要或 Thinking... 上时切换展开
			if !m.hasSelection() && m.toggleToolHit(msg.Y+m.viewport.YOffset) {
				m.sel.lastClickAt = time.Time{}
				return nil
			}
			return m.copySelection()
		}
	}
	return nil
}

func (m *Model) scrollWheel(direction int) {
	if m.composer.selector != nil {
		return
	}
	if direction > 0 {
		m.viewport.ScrollDown(m.viewport.MouseWheelDelta)
	} else if direction < 0 {
		m.viewport.ScrollUp(m.viewport.MouseWheelDelta)
	}
}

func (m *Model) readClipboard() tea.Cmd {
	if m.handlers.ReadClipboard == nil && m.handlers.ReadClipboardData == nil {
		return nil
	}
	readData := m.handlers.ReadClipboardData
	readText := m.handlers.ReadClipboard
	return func() tea.Msg {
		if readData != nil {
			data, err := readData()
			return clipboardReadMsg{data: data, err: err}
		}
		text, err := readText()
		return clipboardReadMsg{data: ClipboardData{Text: text}, err: err}
	}
}

func (m *Model) copySelection() tea.Cmd {
	if !m.hasSelection() || m.handlers.WriteClipboard == nil {
		return nil
	}
	text := m.selectedText()
	if text == "" {
		return nil
	}
	write := m.handlers.WriteClipboard
	return func() tea.Msg { return clipboardWriteMsg{err: write(text)} }
}

func (m *Model) selectedText() string {
	lines := m.frameLines()
	if len(lines) == 0 || !m.hasSelection() {
		return ""
	}
	start, end := m.sel.anchor, m.sel.focus
	if pointBefore(end, start) {
		start, end = end, start
	}
	start.y = clamp(start.y, 0, len(lines)-1)
	end.y = clamp(end.y, 0, len(lines)-1)
	var selected []string
	for y := start.y; y <= end.y; y++ {
		line := ansi.Strip(lines[y])
		width := ansi.StringWidth(line)
		left, right := 0, width
		if y == start.y {
			left = clamp(start.x, 0, width)
		}
		if y == end.y {
			right = clamp(end.x, left, width)
		}
		selected = append(selected, ansi.Cut(line, left, right))
	}
	return strings.TrimRight(strings.Join(selected, "\n"), " \n")
}

func pointBefore(a, b screenPoint) bool {
	return a.y < b.y || (a.y == b.y && a.x < b.x)
}

func mouseIsWheel(msg tea.MouseMsg) bool {
	return msg.Button >= tea.MouseButtonWheelUp && msg.Button <= tea.MouseButtonWheelRight
}

func clamp(value, low, high int) int {
	return min(high, max(low, value))
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func wordBounds(lines []string, point screenPoint) (int, int, bool) {
	if point.y < 0 || point.y >= len(lines) {
		return 0, 0, false
	}
	line := ansi.Strip(lines[point.y])
	if line == "" {
		return 0, 0, false
	}
	type cell struct {
		rune       rune
		start, end int
	}
	var cells []cell
	column := 0
	for _, r := range []rune(line) {
		width := ansi.StringWidth(string(r))
		if width <= 0 {
			width = 1
		}
		cells = append(cells, cell{rune: r, start: column, end: column + width})
		column += width
	}
	if len(cells) == 0 {
		return 0, 0, false
	}
	index := len(cells) - 1
	for i, current := range cells {
		if point.x < current.end {
			index = i
			break
		}
	}
	target := cells[index]
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }
	word := isWord(target.rune)
	left, right := index, index+1
	for left > 0 && isWord(cells[left-1].rune) == word && !unicode.IsSpace(cells[left-1].rune) {
		left--
	}
	for right < len(cells) && isWord(cells[right].rune) == word && !unicode.IsSpace(cells[right].rune) {
		right++
	}
	return cells[left].start, cells[right-1].end, true
}

func paintSelection(lines []string, anchor, focus screenPoint) {
	if anchor == focus {
		return
	}
	if pointBefore(focus, anchor) {
		anchor, focus = focus, anchor
	}
	style := lipgloss.NewStyle().Background(lipgloss.Color(selectionBackground))
	for y := max(0, anchor.y); y <= min(focus.y, len(lines)-1); y++ {
		line := ansi.Strip(lines[y])
		width := ansi.StringWidth(line)
		left, right := 0, width
		if y == anchor.y {
			left = clamp(anchor.x, 0, width)
		}
		if y == focus.y {
			right = clamp(focus.x, left, width)
		}
		if right <= left {
			continue
		}
		lines[y] = ansi.Cut(line, 0, left) + style.Render(ansi.Cut(line, left, right)) + ansi.Cut(line, right, width)
	}
}
