package tui

import (
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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
		m.composer.pasteCounter++
		id := m.composer.pasteCounter
		if m.composer.pastes == nil {
			m.composer.pastes = make(map[int]string)
		}
		m.composer.pastes[id] = text
		m.editor.InsertString(pasteMarker(id, text))
	} else {
		m.editor.InsertString(text)
	}
	m.clearSelection()
	m.composer.completion, m.composer.completionHidden = 0, false
	m.layout()
}

func (m *Model) handleImage(image ImagePart) {
	if image.Data == "" || image.MIMEType == "" {
		return
	}
	if m.composer.images == nil {
		m.composer.images = make(map[int]ImagePart)
	}
	reserved := imageMarkerIDs(m.editor.Value())
	id := nextImageID(&m.composer.imageCounter, reserved)
	m.composer.images[id] = image
	m.editor.InsertString(imageMarker(id))
	m.clearSelection()
	m.composer.completion, m.composer.completionHidden = 0, false
	m.layout()
}

func imageMarker(id int) string {
	return "[Image #" + strconv.Itoa(id) + "]"
}

// buildPayload 展开文本粘贴标记，并按图片占位符位置组装待发送内容
func (m *Model) buildPayload() (ComposerPayload, bool) {
	value := strings.TrimSpace(m.expandPasteMarkers(m.editor.Value()))
	if value == "" {
		return ComposerPayload{}, false
	}
	var text strings.Builder
	var images []ImagePart
	last := 0
	used := make(map[int]bool)
	for _, match := range imageMarkerRegexp.FindAllStringSubmatchIndex(value, -1) {
		if match[0] > last {
			text.WriteString(value[last:match[0]])
		}
		id, err := strconv.Atoi(value[match[2]:match[3]])
		image, ok := m.composer.images[id]
		if err != nil || !ok || used[id] {
			text.WriteString(value[match[0]:match[1]])
		} else {
			images = append(images, image)
			used[id] = true
		}
		last = match[1]
	}
	if last < len(value) {
		text.WriteString(value[last:])
	}
	payload := ComposerPayload{Text: text.String(), Images: images}
	if payload.Text == "" && len(payload.Images) == 0 {
		return ComposerPayload{}, false
	}
	return payload, true
}

func payloadHasImages(payload ComposerPayload) bool {
	return len(payload.Images) > 0
}

// editorTextForPayload 把待发送内容还原成编辑器文本，复用相同的附件编号规则
func (m *Model) editorTextForPayload(payload ComposerPayload) string {
	draft := ComposerDraft{Images: m.composer.images, ImageCounter: m.composer.imageCounter}
	appendPayloadDraft(&draft, payload)
	m.composer.images, m.composer.imageCounter = draft.Images, draft.ImageCounter
	return draft.Text
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

func nextImageID(counter *int, reserved map[int]bool) int {
	for {
		*counter = *counter + 1
		if !reserved[*counter] {
			reserved[*counter] = true
			return *counter
		}
	}
}

func (m *Model) restoreEditorPayload(payload *ComposerPayload, fallback string) {
	m.invalidateHistoryInput()
	m.currentHistory().navigation.lastText = nil
	m.resetPastes()
	value := fallback
	if payload != nil {
		value = m.editorTextForPayload(*payload)
	}
	m.editor.SetValue(value)
	m.editor.CursorEnd()
	m.composer.completion, m.composer.completionHidden = 0, true
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
	if len(m.composer.pastes) == 0 || !strings.Contains(text, "[paste #") {
		return text
	}
	return pasteMarkerRegexp.ReplaceAllStringFunc(text, func(match string) string {
		sub := pasteMarkerRegexp.FindStringSubmatch(match)
		id, err := strconv.Atoi(sub[1])
		if err != nil {
			return match
		}
		if content, ok := m.composer.pastes[id]; ok {
			if match == pasteMarker(id, content) {
				return content
			}
		}
		return match
	})
}

func (m *Model) resetPastes() {
	m.composer.pastes = nil
	m.composer.pasteCounter = 0
	m.composer.images = nil
	m.composer.imageCounter = 0
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
	if _, ok := m.composer.pastes[id]; !ok {
		return false
	}
	// 用 rune 数决定退格次数，避免多字节标记残留半个字符
	steps := len([]rune(prefix[loc[0]:loc[1]]))
	for range steps {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		_ = cmd
	}
	delete(m.composer.pastes, id)
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
	if _, ok := m.composer.images[id]; !ok {
		return false
	}
	for i := 0; i < len([]rune(prefix[loc[0]:loc[1]])); i++ {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		_ = cmd
	}
	delete(m.composer.images, id)
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
