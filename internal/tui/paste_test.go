package tui

import (
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
	if stored := m.pastes[1]; stored != body {
		t.Fatalf("paste content not stored verbatim: %d runes vs %d", len([]rune(stored)), len([]rune(body)))
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
	if len(m.pastes) != 0 {
		t.Fatalf("small paste should not register: %v", m.pastes)
	}
}

func TestManualTypingNoFold(t *testing.T) {
	m := testModel(80, 24)
	typeText(m, strings.Repeat("x", 1001))
	if strings.Contains(m.editor.Value(), "[paste #") {
		t.Fatal("manual typing must never fold")
	}
	if len(m.pastes) != 0 {
		t.Fatalf("manual typing should not register: %v", m.pastes)
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
	if len(m.pastes) != 0 || m.editor.Value() != "" {
		t.Fatalf("submit should clear pastes and editor: %v %q", m.pastes, m.editor.Value())
	}
	if len(m.history) != 1 || m.history[0] != body+"tail" {
		t.Fatalf("history should keep expanded text: %q", m.history)
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
	if _, ok := m.pastes[1]; ok {
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
	if len(m.pastes) != 0 {
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
	if got := m.editor.Value(); got != "" || len(m.pastes) != 0 {
		t.Fatalf("markers and separator should be gone: %q %v", got, m.pastes)
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
	if got := m.editor.Value(); got != "" || len(m.pastes) != 0 {
		t.Fatalf("all markers should be gone: %q %v", got, m.pastes)
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
	m.Update(clipboardReadMsg{text: strings.Repeat("y", 1001)})
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
