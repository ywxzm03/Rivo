package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestPasteFoldLargePaste(t *testing.T) {
	m := testModel(80, 24)
	body := strings.Repeat("line\n", 10) + "line"
	pasteText(m, body)
	if got := m.editor.Value(); !strings.Contains(got, "[paste #1 +11 lines]") {
		t.Fatalf("large paste should fold to marker: %q", got)
	}
	if stored := m.composer.pastes[1]; stored != body {
		t.Fatalf("paste content not stored verbatim: %d runes vs %d", len([]rune(stored)), len([]rune(body)))
	}
}

func TestClipboardImagePasteBuildsStructuredUserMessage(t *testing.T) {
	m := testModel(80, 24)
	m.state.Models[0].SupportsImages = true
	var request Request
	m.handlers = Handlers{Request: func(value Request) tea.Cmd {
		request = value
		return nil
	}}
	typeText(m, "describe ")
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "aGk=", MIMEType: "image/png"}}}})
	press(m, tea.KeyEnter)
	if request.Payload == nil {
		t.Fatal("image submission did not build a payload")
	}
	if request.Payload.Text != "describe " {
		t.Fatalf("payload text = %q, want %q", request.Payload.Text, "describe ")
	}
	if len(request.Payload.Images) != 1 || request.Payload.Images[0].MIMEType != "image/png" {
		t.Fatalf("payload images = %#v, want one png", request.Payload.Images)
	}
}

func TestImageOnlyPasteIsVisibleAndRequiresVisionModel(t *testing.T) {
	m := testModel(80, 24)
	called := false
	m.handlers = Handlers{Request: func(Request) tea.Cmd {
		called = true
		return nil
	}}
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "aGk=", MIMEType: "image/png"}}}})
	if !strings.Contains(m.editor.Value(), "[Image #1]") {
		t.Fatalf("image placeholder missing: %q", m.editor.Value())
	}
	press(m, tea.KeyEnter)
	if called {
		t.Fatal("non-vision model accepted image input")
	}
	if m.editor.Value() != "[Image #1]" {
		t.Fatalf("rejected image draft was cleared: %q", m.editor.Value())
	}
}

func TestImageHistoryRestoresAttachment(t *testing.T) {
	m := testModel(80, 24)
	m.state.Models[0].SupportsImages = true
	m.handlers = Handlers{Request: func(Request) tea.Cmd { return nil }}
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "aGk=", MIMEType: "image/png"}}}})
	press(m, tea.KeyEnter)
	m.busy = false
	press(m, tea.KeyUp)
	if m.editor.Value() != "[Image #1]" || len(m.composer.images) != 1 {
		t.Fatalf("history did not restore image draft: value=%q images=%d", m.editor.Value(), len(m.composer.images))
	}
}

func TestImageHistoryIgnoresLiteralImageMarkers(t *testing.T) {
	m := testModel(80, 24)
	m.state.Models[0].SupportsImages = true
	m.handlers = Handlers{Request: func(Request) tea.Cmd { return nil }}
	typeText(m, "literal [Image #1] then ")
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "aGk=", MIMEType: "image/png"}}}})
	press(m, tea.KeyEnter)
	m.busy = false
	press(m, tea.KeyUp)
	payload, ok := m.buildPayload()
	if !ok || !payloadHasImages(payload) || len(m.composer.images) != 1 {
		t.Fatalf("history image restore = payload=%#v images=%d", payload, len(m.composer.images))
	}
	if _, ok := m.composer.images[2]; !ok {
		t.Fatalf("restored image map = %#v, want actual image marker #2", m.composer.images)
	}
	if len(payload.Images) == 0 {
		t.Fatalf("restored payload = %#v, want image after literal marker", payload)
	}
}

func TestBackspaceDeletesLastImageMarker(t *testing.T) {
	m := testModel(80, 24)
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{
		{Data: "one", MIMEType: "image/png"},
		{Data: "two", MIMEType: "image/png"},
	}}})
	press(m, tea.KeyBackspace)
	if got := m.editor.Value(); got != "[Image #1]" {
		t.Fatalf("editor after deleting last image = %q", got)
	}
	if len(m.composer.images) != 1 {
		t.Fatalf("images after deleting last image = %d, want 1", len(m.composer.images))
	}
}

func TestSteeringImageEnqueueFailureRestoresAttachment(t *testing.T) {
	m := testModel(80, 24)
	m.state.Models[0].SupportsImages = true
	m.busy = true
	m.handlers = Handlers{EnqueueSteeringMessage: func(ComposerPayload) error {
		return errors.New("queue unavailable")
	}}
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "aGk=", MIMEType: "image/png"}}}})
	press(m, tea.KeyEnter)
	if m.editor.Value() != "[Image #1]" || len(m.composer.images) != 1 {
		t.Fatalf("failed steering restore = value=%q images=%d", m.editor.Value(), len(m.composer.images))
	}
}

func TestPasteFoldCharLimit(t *testing.T) {
	m := testModel(80, 24)
	body := strings.Repeat("x", 1001)
	pasteText(m, body)
	if got := m.editor.Value(); !strings.Contains(got, "[paste #1 1001 chars]") {
		t.Fatalf("over 1000 chars should fold: %q", got)
	}
}

func TestPasteNoFoldSmall(t *testing.T) {
	m := testModel(80, 24)
	body := strings.Repeat("line\n", 4) + "end"
	pasteText(m, body)
	if got := m.editor.Value(); got != body {
		t.Fatalf("small paste should insert verbatim: %q", got)
	}
	if len(m.composer.pastes) != 0 {
		t.Fatalf("small paste should not register: %v", m.composer.pastes)
	}
}

func TestPastePreservesTerminalLikeText(t *testing.T) {
	m := testModel(80, 24)
	pasteText(m, "arr[M] [<tag>\x01")
	if got := m.editor.Value(); got != "arr[M] [<tag>" {
		t.Fatalf("paste text = %q, want terminal-like text preserved and control byte cleaned", got)
	}
}

func TestManualTypingNoFold(t *testing.T) {
	m := testModel(80, 24)
	typeText(m, strings.Repeat("x", 1001))
	if strings.Contains(m.editor.Value(), "[paste #") {
		t.Fatal("manual typing must never fold")
	}
	if len(m.composer.pastes) != 0 {
		t.Fatalf("manual typing should not register: %v", m.composer.pastes)
	}
}

func TestSubmitExpandsPaste(t *testing.T) {
	var dispatched string
	m := testModel(80, 24)
	m.handlers = Handlers{Request: func(r Request) tea.Cmd {
		dispatched = r.Value
		return nil
	}}
	body := strings.Repeat("line\n", 10) + "line"
	pasteText(m, body)
	pasteText(m, "tail")
	press(m, tea.KeyEnter)
	if dispatched != body+"tail" {
		t.Fatalf("submit should expand markers, got %q", dispatched)
	}
	if len(m.composer.pastes) != 0 || m.editor.Value() != "" {
		t.Fatalf("submit should clear pastes and editor: %v %q", m.composer.pastes, m.editor.Value())
	}
	if len(m.composer.history) != 1 || m.composer.history[0] != body+"tail" {
		t.Fatalf("history should keep expanded text: %q", m.composer.history)
	}
}

func TestSubmitLiteralMarker(t *testing.T) {
	var dispatched string
	m := testModel(80, 24)
	m.handlers = Handlers{Request: func(r Request) tea.Cmd {
		dispatched = r.Value
		return nil
	}}
	typeText(m, "[paste #1 +169 lines]")
	press(m, tea.KeyEnter)
	if dispatched != "[paste #1 +169 lines]" {
		t.Fatalf("literal marker text must pass through unchanged: %q", dispatched)
	}
}

func TestBackspaceDeletesMarker(t *testing.T) {
	m := testModel(80, 24)
	pasteText(m, strings.Repeat("line\n", 10)+"line")
	typeText(m, "after")
	// 光标在 "after" 末尾，先退 5 次到标记尾部
	for i := 0; i < 5; i++ {
		press(m, tea.KeyBackspace)
	}
	press(m, tea.KeyBackspace)
	if strings.Contains(m.editor.Value(), "[paste #") {
		t.Fatalf("marker should be deleted atomically: %q", m.editor.Value())
	}
	if _, ok := m.composer.pastes[1]; ok {
		t.Fatal("registry entry should be removed with the marker")
	}
}

func TestBackspaceDeletesMarkerAfterMultibytePrefix(t *testing.T) {
	m := testModel(80, 24)
	pasteText(m, "中文前缀")
	pasteText(m, strings.Repeat("line\n", 10)+"line")
	press(m, tea.KeyBackspace)
	if got := m.editor.Value(); got != "中文前缀" {
		t.Fatalf("multibyte marker deletion = %q", got)
	}
	if len(m.composer.pastes) != 0 {
		t.Fatal("deleted marker remained registered")
	}
}

func TestDeleteNoRenumber(t *testing.T) {
	m := testModel(80, 24)
	pasteText(m, strings.Repeat("a\n", 10)+"a")
	pasteText(m, " ")
	pasteText(m, strings.Repeat("b\n", 10)+"b")
	// 光标在第二个标记尾部；依次整删 #2、空格、#1，编辑器应清空且注册表随之清空
	press(m, tea.KeyBackspace)
	press(m, tea.KeyBackspace)
	press(m, tea.KeyBackspace)
	if got := m.editor.Value(); got != "" || len(m.composer.pastes) != 0 {
		t.Fatalf("markers and separator should be gone: %q %v", got, m.composer.pastes)
	}
	// 重新粘贴两次（id 从 #3 继续），验证删除其中一个后剩余标记仍按原 id 展开：
	// 用 ctrl+a 回到行首，再从行首向后逐字符左移不可行；改为整删 #4（尾部 backspace），
	// 然后普通退格删空格，再整删 #3，最后重新粘贴 #3 内容并提交
	pasteText(m, strings.Repeat("a\n", 10)+"a")
	pasteText(m, " ")
	pasteText(m, strings.Repeat("b\n", 10)+"b")
	press(m, tea.KeyBackspace) // 整删 #4
	if strings.Contains(m.editor.Value(), "paste #4") {
		t.Fatalf("fourth marker should be deleted atomically: %q", m.editor.Value())
	}
	press(m, tea.KeyBackspace) // 删空格
	press(m, tea.KeyBackspace) // 整删 #3
	if got := m.editor.Value(); got != "" || len(m.composer.pastes) != 0 {
		t.Fatalf("all markers should be gone: %q %v", got, m.composer.pastes)
	}
	pasteText(m, strings.Repeat("a\n", 10)+"a")
	var dispatched string
	m.handlers = Handlers{Request: func(r Request) tea.Cmd {
		dispatched = r.Value
		return nil
	}}
	press(m, tea.KeyEnter)
	if strings.Contains(dispatched, "[paste #") || !strings.HasSuffix(dispatched, strings.Repeat("a\n", 10)+"a") {
		t.Fatalf("re-pasted content should expand to full text: %q", dispatched)
	}
}

func TestClipboardPasteFolds(t *testing.T) {
	m := testModel(80, 24)
	m.Update(clipboardReadMsg{data: ClipboardData{Text: strings.Repeat("y", 1001)}})
	if got := m.editor.Value(); !strings.Contains(got, "[paste #1 1001 chars]") {
		t.Fatalf("clipboard paste should fold: %q", got)
	}
}

func TestFooterSingleLineStillGreen(t *testing.T) {
	m := testModel(120, 30)
	footer := ansi.Strip(m.footer())
	if strings.Count(footer, "\n") != 0 {
		t.Fatalf("footer single line regression: %q", footer)
	}
}
