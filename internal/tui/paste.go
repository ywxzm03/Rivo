package tui

import (
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// 大粘贴折叠阈值，与 Pi v1.0.0 editor.ts 保持一致
const (
	pasteMaxLines = 10
	pasteMaxChars = 1000
	pasteTabWidth = 4
)

// pasteMarkerRegexp 匹配折叠标记；展开时按精确 id 替换，字面文本不受影响
var pasteMarkerRegexp = regexp.MustCompile(`\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]`)

// pasteTailMarkerRegexp 匹配紧贴文本末尾的完整标记，用于光标整删判断；捕获组 2 是 id
var pasteTailMarkerRegexp = regexp.MustCompile(`\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]$`)

// handlePaste 处理一次终端粘贴：超阈值折叠为标记，否则原样插入
func (m *Model) handlePaste(text string) {
	text = cleanPasteText(text)
	if text == "" {
		return
	}
	lines := strings.Count(text, "\n") + 1
	runes := len([]rune(text))
	if lines > pasteMaxLines || runes > pasteMaxChars {
		m.pasteCounter++
		id := m.pasteCounter
		if m.pastes == nil {
			m.pastes = make(map[int]string)
		}
		m.pastes[id] = text
		var marker string
		if lines > pasteMaxLines {
			marker = "[paste #" + strconv.Itoa(id) + " +" + strconv.Itoa(lines) + " lines]"
		} else {
			marker = "[paste #" + strconv.Itoa(id) + " " + strconv.Itoa(runes) + " chars]"
		}
		m.editor.InsertString(marker)
	} else {
		m.editor.InsertString(text)
	}
	m.clearSelection()
	m.completion, m.completionHidden = 0, false
	m.layout()
}

// cleanPasteText 规范换行、展开制表符并剔除控制字符（保留换行）
func cleanPasteText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\t", strings.Repeat(" ", pasteTabWidth))
	var b strings.Builder
	for _, r := range text {
		if r == '\n' || r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// expandPasteMarkers 把标记替换回注册表中的完整内容；无对应 id 的字面文本原样保留
func (m *Model) expandPasteMarkers(text string) string {
	if len(m.pastes) == 0 || !strings.Contains(text, "[paste #") {
		return text
	}
	return pasteMarkerRegexp.ReplaceAllStringFunc(text, func(match string) string {
		sub := pasteMarkerRegexp.FindStringSubmatch(match)
		id, err := strconv.Atoi(sub[1])
		if err != nil {
			return match
		}
		if content, ok := m.pastes[id]; ok {
			return content
		}
		return match
	})
}

// resetPastes 清空注册表；编辑器重置或提交后调用
func (m *Model) resetPastes() {
	m.pastes = nil
	m.pasteCounter = 0
}

// backspaceAtMarker 判断光标是否紧邻标记尾部；是则整块删除标记并移除注册项
func (m *Model) backspaceAtMarker() bool {
	lineIndex := m.editor.Line()
	lines := strings.Split(m.editor.Value(), "\n")
	if lineIndex < 0 || lineIndex >= len(lines) {
		return false
	}
	info := m.editor.LineInfo()
	col := info.StartColumn + info.ColumnOffset
	current := lines[lineIndex]
	runes := []rune(current)
	col = min(col, len(runes))
	prefix := string(runes[:col])
	// 锚定 prefix 末尾：光标左侧必须是一个完整标记；group 1 是 id
	loc := pasteTailMarkerRegexp.FindStringSubmatchIndex(prefix)
	if loc == nil {
		return false
	}
	id, err := strconv.Atoi(prefix[loc[2]:loc[3]])
	if err != nil {
		return false
	}
	if _, ok := m.pastes[id]; !ok {
		return false
	}
	// 用 rune 数决定退格次数，避免多字节标记残留半个字符
	steps := len([]rune(prefix[loc[0]:loc[1]]))
	for i := 0; i < steps; i++ {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		_ = cmd
	}
	delete(m.pastes, id)
	return true
}
