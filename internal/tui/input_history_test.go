package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// historyTestModel 用真实按键提交本地输入，模型运行由已有请求入口隔离
func historyTestModel(inputs ...string) *Model {
	m := testModel(80, 24)
	m.handlers.Request = func(Request) tea.Cmd { return nil }
	for _, input := range inputs {
		typeText(m, input)
		press(m, tea.KeyEnter)
		m.busy = false
	}
	return m
}

func assertHistoryText(t *testing.T, m *Model, want string) {
	t.Helper()
	if got := m.editor.Value(); got != want {
		t.Fatalf("editor = %q, want %q", got, want)
	}
}

// eraseHistoryDraft 通过编辑按键清空，保留已确认的历史浏览位置
func eraseHistoryDraft(m *Model) {
	press(m, tea.KeyEnd)
	for range utf8.RuneCountInString(m.editor.Value()) {
		press(m, tea.KeyBackspace)
	}
}

func TestInputHistoryEmptyRecallOrder(t *testing.T) {
	m := historyTestModel("alpha", "beta")
	for _, step := range []struct {
		key  tea.KeyType
		want string
	}{
		{tea.KeyDown, ""},
		{tea.KeyUp, "beta"},
		{tea.KeyUp, "alpha"},
		{tea.KeyDown, "beta"},
		{tea.KeyDown, ""},
		{tea.KeyUp, "beta"},
	} {
		press(m, step.key)
		assertHistoryText(t, m, step.want)
		if step.want != "" {
			line := m.editor.LineInfo()
			if got := line.StartColumn + line.ColumnOffset; got != utf8.RuneCountInString(step.want) {
				t.Fatalf("recalled cursor rune column = %d, want end of %q", got, step.want)
			}
		}
	}
}

func TestInputHistoryDraftAndAbsoluteBoundary(t *testing.T) {
	t.Run("fresh drafts remain editable", func(t *testing.T) {
		for _, draft := range []string{"new draft", "   ", "first\nsecond", strings.Repeat("a", 79)} {
			t.Run(draft, func(t *testing.T) {
				m := historyTestModel("older")
				typeText(m, draft)
				press(m, tea.KeyHome)
				press(m, tea.KeyUp)
				assertHistoryText(t, m, draft)
				press(m, tea.KeyDown)
				assertHistoryText(t, m, draft)
			})
		}
	})
	t.Run("multiline end immediately recalls older entry", func(t *testing.T) {
		m := historyTestModel("older", "first\nsecond\nlast")
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "first\nsecond\nlast")
		if m.editor.Line() != 2 {
			t.Fatalf("recalled logical line = %d, want 2", m.editor.Line())
		}
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "older")
	})
	t.Run("interior cursor only moves within recalled text", func(t *testing.T) {
		m := historyTestModel("older", "first\nsecond\nlast")
		press(m, tea.KeyUp)
		press(m, tea.KeyHome)
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "first\nsecond\nlast")
		if m.editor.Line() != 1 {
			t.Fatalf("interior Up moved to logical line %d, want 1", m.editor.Line())
		}
	})
}

func TestInputHistoryUnicodeBoundary(t *testing.T) {
	for _, input := range []string{"中文🙂e\u0301", "第一行\n第二行🙂e\u0301", strings.Repeat("宽🙂", 28)} {
		t.Run(input, func(t *testing.T) {
			m := historyTestModel("older", input)
			press(m, tea.KeyUp)
			assertHistoryText(t, m, input)
			m.Update(tea.WindowSizeMsg{Width: 37, Height: 24})
			press(m, tea.KeyLeft)
			press(m, tea.KeyUp)
			assertHistoryText(t, m, input)
			// 终端宽度只影响显示行，重新回到整段末尾仍应连续召回
			m.editor.SetValue(input)
			m.editor.CursorEnd()
			press(m, tea.KeyUp)
			assertHistoryText(t, m, "older")
		})
	}
}

func TestInputHistoryEditKeepsCursor(t *testing.T) {
	t.Run("edited nonempty text is not replaced", func(t *testing.T) {
		m := historyTestModel("alpha", "beta")
		press(m, tea.KeyUp)
		typeText(m, "!")
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "beta!")
	})
	t.Run("erasing an edit continues from confirmed position", func(t *testing.T) {
		m := historyTestModel("alpha", "beta")
		press(m, tea.KeyUp)
		typeText(m, "!")
		eraseHistoryDraft(m)
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "alpha")
	})
	t.Run("restoring exact text permits continued recall", func(t *testing.T) {
		m := historyTestModel("alpha", "beta")
		press(m, tea.KeyUp)
		typeText(m, "!")
		press(m, tea.KeyBackspace)
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "alpha")
	})
}

func TestInputHistoryOldestFallsBackToEditor(t *testing.T) {
	m := historyTestModel("first\nlast")
	press(m, tea.KeyUp)
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "first\nlast")
	if m.editor.Line() != 0 {
		t.Fatalf("Up at oldest entry left cursor on logical line %d, want 0", m.editor.Line())
	}
}

func TestInputHistoryClearedRichDraft(t *testing.T) {
	m := historyTestModel()
	m.clock = func() time.Time { return time.Unix(1000, 0) }
	m.state.Models[0].SupportsImages = true
	body := strings.Repeat("粘贴内容\n", 11) + "尾行"
	typeText(m, "before ")
	pasteText(m, body)
	typeText(m, " middle ")
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "first-image", MIMEType: "image/png"}}}})
	typeText(m, " after")
	want := m.editor.Value()
	press(m, tea.KeyCtrlC)
	assertHistoryText(t, m, "")
	press(m, tea.KeyUp)
	assertHistoryText(t, m, want)
	if m.composer.pastes[1] != body || m.composer.images[1].Data != "first-image" {
		t.Fatalf("rich recall lost ownership: pastes=%v images=%v", m.composer.pastes, m.composer.images)
	}
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "second-image", MIMEType: "image/png"}}}})
	if m.composer.images[1].Data != "first-image" || m.composer.images[2].Data != "second-image" {
		t.Fatalf("new image reused recalled marker: %v", m.composer.images)
	}
	// 清空活动副本后重新召回，保存的条目不应被后续编辑污染
	eraseHistoryDraft(m)
	press(m, tea.KeyDown)
	press(m, tea.KeyUp)
	assertHistoryText(t, m, want)
	if len(m.composer.images) != 1 || m.composer.images[1].Data != "first-image" {
		t.Fatalf("saved image registry was mutated: %v", m.composer.images)
	}
	payload, ok := m.buildPayload()
	if !ok || payload.Text != "before "+body+" middle  after" || len(payload.Images) != 1 || payload.Images[0].Data != "first-image" {
		t.Fatalf("restored submission = %#v, valid=%v", payload, ok)
	}
}

func TestInputHistorySubmittedImagesKeepOriginalPosition(t *testing.T) {
	m := historyTestModel()
	m.state.Models[0].SupportsImages = true
	typeText(m, "literal [Image #1] before ")
	m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "actual", MIMEType: "image/png"}}}})
	typeText(m, " after")
	want := m.editor.Value()
	press(m, tea.KeyEnter)
	m.busy = false
	press(m, tea.KeyUp)
	assertHistoryText(t, m, want)
	if len(m.composer.images) != 1 || m.composer.images[2].Data != "actual" {
		t.Fatalf("literal marker gained image ownership: %v", m.composer.images)
	}
}

func TestInputHistoryLocalDedupAndReplaySkip(t *testing.T) {
	t.Run("only adjacent equal drafts collapse", func(t *testing.T) {
		m := historyTestModel("alpha", "alpha", "beta", "alpha")
		for _, want := range []string{"alpha", "beta", "alpha", "alpha"} {
			press(m, tea.KeyUp)
			assertHistoryText(t, m, want)
		}
		press(m, tea.KeyDown)
		assertHistoryText(t, m, "beta")
	})
	t.Run("different images remain distinct", func(t *testing.T) {
		m := historyTestModel()
		m.state.Models[0].SupportsImages = true
		for _, data := range []string{"first", "second"} {
			m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: data, MIMEType: "image/png"}}}})
			press(m, tea.KeyEnter)
			m.busy = false
		}
		for _, want := range []string{"second", "first"} {
			press(m, tea.KeyUp)
			payload, ok := m.buildPayload()
			if !ok || len(payload.Images) != 1 || payload.Images[0].Data != want {
				t.Fatalf("recalled image = %#v, valid=%v, want %q", payload, ok, want)
			}
		}
	})
}

func TestTaskFocusFromEmptyLeft(t *testing.T) {
	task := SubagentTaskView{ID: "child", AgentType: "work", Status: "running", Description: "child work"}
	t.Run("Up recalls input even with tasks", func(t *testing.T) {
		m := historyTestModel("previous")
		m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{task}})
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "previous")
		if m.panel.taskSelector || !m.editor.Focused() {
			t.Fatal("history recall transferred editor focus to task list")
		}
	})
	t.Run("Left focuses existing anchor and Esc returns to input", func(t *testing.T) {
		m := historyTestModel()
		m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{task}})
		press(m, tea.KeyLeft)
		if !m.panel.taskSelector || !m.panel.mainSelected || m.panel.selectedTaskID != "" {
			t.Fatalf("Left moved or failed to focus main anchor: %+v", m.panel)
		}
		press(m, tea.KeyDown)
		if m.panel.selectedTaskID != "child" {
			t.Fatalf("focused Down selected %q, want child", m.panel.selectedTaskID)
		}
		press(m, tea.KeyEsc)
		if m.panel.taskSelector || !m.editor.Focused() {
			t.Fatal("Esc did not return focus to editor")
		}
	})
	t.Run("whitespace blocks Left and unfocused x is text", func(t *testing.T) {
		m := historyTestModel()
		m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{task}})
		typeText(m, " ")
		press(m, tea.KeyLeft)
		if m.panel.taskSelector {
			t.Fatal("Left treated whitespace draft as empty")
		}
		eraseHistoryDraft(m)
		typeText(m, "x")
		assertHistoryText(t, m, "x")
		if m.panel.taskSelector {
			t.Fatal("typing x focused task list")
		}
	})
}

func TestInputHistorySlashAndClear(t *testing.T) {
	t.Run("clear preserves previous local input", func(t *testing.T) {
		m := historyTestModel("previous")
		typeText(m, "/clear")
		press(m, tea.KeyEnter)
		m.Update(OperationDoneMsg{ID: m.operationID, Snapshot: &Snapshot{}, ResetTranscript: true})
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "previous")
	})
	t.Run("unknown command keeps draft without a history entry", func(t *testing.T) {
		m := historyTestModel("previous")
		input := "/zzzz-unrecognized-command"
		typeText(m, input)
		press(m, tea.KeyEnter)
		assertHistoryText(t, m, input)
		eraseHistoryDraft(m)
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "previous")
	})
	t.Run("known unavailable command enters local history", func(t *testing.T) {
		m := historyTestModel("previous")
		m.busy = true
		typeText(m, "/model ")
		press(m, tea.KeyEnter)
		assertHistoryText(t, m, "/model ")
		eraseHistoryDraft(m)
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "/model")
	})
	t.Run("completion records full selected command", func(t *testing.T) {
		m := historyTestModel()
		typeText(m, "/mod")
		press(m, tea.KeyEnter)
		press(m, tea.KeyEsc)
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "/model")
	})
}
