package tui

import (
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// 大粘贴折叠阈值
const (
	pasteMaxLines = 10
	pasteMaxChars = 1000
	pasteTabWidth = 4
)

// pasteMarkerRegexp 匹配带折叠统计的生成标记，不带统计的字面文本不展开
var pasteMarkerRegexp = regexp.MustCompile(`\[paste #(\d+)( \+\d+ lines| \d+ chars)\]`)

// pasteTailMarkerRegexp 匹配紧贴文本末尾的完整标记，用于整删判断，捕获组 1 是 id
var pasteTailMarkerRegexp = regexp.MustCompile(`\[paste #(\d+)( \+\d+ lines| \d+ chars)\]$`)

var imageMarkerRegexp = regexp.MustCompile(`\[Image #(\d+)\]`)
var imageMarkerTailRegexp = regexp.MustCompile(`\[Image #(\d+)\]$`)

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
		m.editor.InsertString(pasteMarker(id, text))
	} else {
		m.editor.InsertString(text)
	}
	m.clearSelection()
	m.completion, m.completionHidden = 0, false
	m.layout()
}

func (m *Model) handleImage(image llm.ImageContent) {
	if image.Data == "" || image.MIMEType == "" {
		return
	}
	if m.images == nil {
		m.images = make(map[int]llm.ImageContent)
	}
	reserved := imageMarkerIDs(m.editor.Value())
	id := m.nextImageID(reserved)
	m.images[id] = image
	m.editor.InsertString(imageMarker(id))
	m.clearSelection()
	m.completion, m.completionHidden = 0, false
	m.layout()
}

func imageMarker(id int) string {
	return "[Image #" + strconv.Itoa(id) + "]"
}

// buildUserMessage 展开文本粘贴标记，并按图片占位符位置组装用户消息
func (m *Model) buildUserMessage() (llm.UserMessage, bool) {
	value := strings.TrimSpace(m.expandPasteMarkers(m.editor.Value()))
	if value == "" {
		return llm.UserMessage{}, false
	}
	var content []llm.Content
	last := 0
	used := make(map[int]bool)
	for _, match := range imageMarkerRegexp.FindAllStringSubmatchIndex(value, -1) {
		if match[0] > last {
			content = append(content, llm.TextContent{Text: value[last:match[0]]})
		}
		id, err := strconv.Atoi(value[match[2]:match[3]])
		image, ok := m.images[id]
		if err != nil || !ok || used[id] {
			content = append(content, llm.TextContent{Text: value[match[0]:match[1]]})
		} else {
			content = append(content, image)
			used[id] = true
		}
		last = match[1]
	}
	if last < len(value) {
		content = append(content, llm.TextContent{Text: value[last:]})
	}
	if len(content) == 0 {
		return llm.UserMessage{}, false
	}
	return llm.UserMessage{Content: content}, true
}

func messageHasImages(message llm.UserMessage) bool {
	for _, content := range message.Content {
		if _, ok := content.(llm.ImageContent); ok {
			return true
		}
	}
	return false
}

func (m *Model) editorTextForMessage(message llm.UserMessage) string {
	reserved := make(map[int]bool)
	for _, content := range message.Content {
		text, ok := content.(llm.TextContent)
		if ok {
			for id := range imageMarkerIDs(text.Text) {
				reserved[id] = true
			}
		}
	}
	var b strings.Builder
	for _, content := range message.Content {
		switch content := content.(type) {
		case llm.TextContent:
			b.WriteString(content.Text)
		case llm.ImageContent:
			if m.images == nil {
				m.images = make(map[int]llm.ImageContent)
			}
			id := m.nextImageID(reserved)
			m.images[id] = content
			b.WriteString(imageMarker(id))
		}
	}
	return b.String()
}

func imageMarkerIDs(text string) map[int]bool {
	ids := make(map[int]bool)
	for _, match := range imageMarkerRegexp.FindAllStringSubmatch(text, -1) {
		id, err := strconv.Atoi(match[1])
		if err == nil {
			ids[id] = true
		}
	}
	return ids
}

func (m *Model) nextImageID(reserved map[int]bool) int {
	for {
		m.imageCounter++
		if !reserved[m.imageCounter] {
			reserved[m.imageCounter] = true
			return m.imageCounter
		}
	}
}

func (m *Model) restoreEditorMessage(message *llm.UserMessage, fallback string) {
	m.resetPastes()
	value := fallback
	if message != nil {
		value = m.editorTextForMessage(*message)
	}
	m.editor.SetValue(value)
	m.editor.CursorEnd()
	m.completion, m.completionHidden = 0, true
}

func pasteMarker(id int, text string) string {
	lines := strings.Count(text, "\n") + 1
	if lines > pasteMaxLines {
		return "[paste #" + strconv.Itoa(id) + " +" + strconv.Itoa(lines) + " lines]"
	}
	return "[paste #" + strconv.Itoa(id) + " " + strconv.Itoa(len([]rune(text))) + " chars]"
}

// cleanPasteText 规范换行、展开制表符并剔除控制字符
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

// expandPasteMarkers 把标记替换回注册表内容，无对应 id 的字面文本原样保留
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
			if match == pasteMarker(id, content) {
				return content
			}
		}
		return match
	})
}

func (m *Model) resetPastes() {
	m.pastes = nil
	m.pasteCounter = 0
	m.images = nil
	m.imageCounter = 0
}

// backspaceAtMarker 判断光标是否紧邻标记尾部，是则整块删除标记并移除注册项
func (m *Model) backspaceAtMarker() bool {
	if m.backspaceAtImageMarker() {
		return true
	}
	lineIndex := m.editor.Line()
	lines := strings.Split(m.editor.Value(), "\n")
	if lineIndex < 0 || lineIndex >= len(lines) {
		return false
	}
	info := m.editor.LineInfo()
	current := lines[lineIndex]
	runes := []rune(current)
	start := min(max(0, info.StartColumn), len(runes))
	col := min(start+runeOffsetAtWidth(runes[start:], info.CharOffset), len(runes))
	prefix := string(runes[:col])
	// 锚定 prefix 末尾：光标左侧必须是一个完整标记，group 1 是 id
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

func (m *Model) backspaceAtImageMarker() bool {
	lineIndex := m.editor.Line()
	lines := strings.Split(m.editor.Value(), "\n")
	if lineIndex < 0 || lineIndex >= len(lines) {
		return false
	}
	info := m.editor.LineInfo()
	runes := []rune(lines[lineIndex])
	start := min(max(0, info.StartColumn), len(runes))
	col := min(start+runeOffsetAtWidth(runes[start:], info.CharOffset), len(runes))
	prefix := string(runes[:col])
	loc := imageMarkerTailRegexp.FindStringSubmatchIndex(prefix)
	if loc == nil {
		return false
	}
	id, err := strconv.Atoi(prefix[loc[2]:loc[3]])
	if err != nil {
		return false
	}
	if _, ok := m.images[id]; !ok {
		return false
	}
	for i := 0; i < len([]rune(prefix[loc[0]:loc[1]])); i++ {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		_ = cmd
	}
	delete(m.images, id)
	return true
}

func runeOffsetAtWidth(runes []rune, width int) int {
	if width < 0 {
		return 0
	}
	for i := range runes {
		used := ansi.StringWidth(string(runes[:i+1]))
		if used > width {
			return i
		}
		// 继续吸收组合字符与连接符，避免在同一视觉字符内部停下
		if used == width {
			continue
		}
	}
	return len(runes)
}
