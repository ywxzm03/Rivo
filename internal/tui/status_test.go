package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func statusReport() SessionStatusMsg {
	return SessionStatusMsg{
		Model: "deepseek-flash", Provider: "deepseek",
		ThreadName: "Status command", SessionID: "0f8c1c1e-4a1f-4a54-9d5f-1c1f7b5f0a11",
		Transcript: "/Users/me/.rivo/agent/sessions/--proj--/2026-10-24_10-00-00_0f8c.jsonl",
		Tokens:     324_000, ContextWindow: 1_000_000, Available: true,
	}
}

func TestFormatStatusAlignsValueColumn(t *testing.T) {
	lines := strings.Split(formatStatus(statusReport()), "\n")
	want := []string{
		"Model:             deepseek-flash",
		"Provider:          deepseek",
		"Context window:    32.4% used (324K / 1M)",
		"Thread name:       Status command",
		"Session:           0f8c1c1e-4a1f-4a54-9d5f-1c1f7b5f0a11",
		"Transcript:        /Users/me/.rivo/agent/sessions/--proj--/2026-10-24_10-00-00_0f8c.jsonl",
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %d, want %d: %q", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestFormatStatusUnavailableAndUnnamed(t *testing.T) {
	got := formatStatus(SessionStatusMsg{Model: "m", Provider: "p", SessionID: "s", Transcript: "t"})
	if !strings.Contains(got, "\nContext window:    unavailable\n") || !strings.Contains(got, "\nThread name:       (unnamed)\n") {
		t.Fatalf("status = %q", got)
	}
}

func TestFormatStatusTokens(t *testing.T) {
	for tokens, want := range map[int]string{
		0: "0", 999: "999", 1000: "1K", 1_500: "1.5K", 324_000: "324K",
		1_000_000: "1M", 1_250_000: "1.2M",
	} {
		if got := formatStatusTokens(tokens); got != want {
			t.Errorf("formatStatusTokens(%d) = %q, want %q", tokens, got, want)
		}
	}
}

func TestStatusCommandAppendsRuntimeLine(t *testing.T) {
	m := testModel(120, 30)
	m.handlers.Request = func(Request) tea.Cmd { return nil }
	called := false
	m.handlers.Status = func() tea.Cmd {
		called = true
		return func() tea.Msg { return statusReport() }
	}
	m.editor.SetValue("/status")
	cmd := m.submit()
	if !called || cmd == nil {
		t.Fatalf("status command called=%v cmd=%v", called, cmd != nil)
	}
	m.Update(cmd())
	if len(m.state.Messages) != 2 {
		t.Fatalf("messages = %+v, want command echo and status block", m.state.Messages)
	}
	echo, last := m.state.Messages[0], m.state.Messages[1]
	if echo.Role != "command" || echo.Text != "/status" {
		t.Fatalf("command echo = %+v", echo)
	}
	if last.Role != "status" || !strings.Contains(last.Text, "Context window:    32.4% used (324K / 1M)") {
		t.Fatalf("status line = %+v", last)
	}
	if rendered := ansi.Strip(m.renderMessage(last)); !strings.Contains(rendered, "Thread name:       Status command") {
		t.Fatalf("status render = %q", rendered)
	}
	// 回显使用紫色 accent，与状态正文的白色区分
	if echoed := m.renderMessage(echo); echoed != colored(accentColor, "/status") {
		t.Fatalf("command echo render = %q, want accent color", echoed)
	}
}

func TestStatusCommandAppendsOnEveryExecution(t *testing.T) {
	m := testModel(120, 30)
	m.Update(statusReport())
	first := m.state.Messages
	next := statusReport()
	next.Tokens = 500_000
	m.Update(next)
	if len(m.state.Messages) != 4 {
		t.Fatalf("status messages = %d, want two command/status pairs", len(m.state.Messages))
	}
	for _, message := range first {
		if _, ok := m.messageIndex[message.ID]; !ok {
			t.Fatalf("earlier status rows were replaced: %+v", m.state.Messages)
		}
	}
	latest := m.state.Messages[len(m.state.Messages)-1]
	if latest.ID == first[len(first)-1].ID || !strings.Contains(latest.Text, "50.0% used (500K / 1M)") {
		t.Fatalf("latest status block = %+v", latest)
	}
}

func TestStatusCommandRejectsBusyAndArguments(t *testing.T) {
	m := testModel(120, 30)
	called := false
	m.handlers.Status = func() tea.Cmd {
		called = true
		return nil
	}
	m.busy = true
	m.editor.SetValue("/status")
	m.submit()
	if called || len(m.state.Messages) == 0 || !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "Commands are unavailable") {
		t.Fatalf("busy status command called=%v messages=%+v", called, m.state.Messages)
	}

	m = testModel(120, 30)
	m.handlers.Request = func(Request) tea.Cmd { return nil }
	m.handlers.Status = func() tea.Cmd {
		called = true
		return nil
	}
	m.editor.SetValue("/status extra")
	m.submit()
	if len(m.state.Messages) == 0 || m.state.Messages[len(m.state.Messages)-1].Text != "Usage: /status" {
		t.Fatalf("argument error = %+v", m.state.Messages)
	}
}

func TestLastMessageIDSkipsRuntimeOnlyRows(t *testing.T) {
	m := testModel(100, 24)
	m.state.Messages = []Message{
		{ID: "assistant", Role: "assistant", Text: "answer"},
		{ID: "status-command/1", Role: "command", Text: "/status"},
		{ID: "status/1", Role: "status", Text: "Model: m"},
	}
	if got := m.lastMessageID(); got != "assistant" {
		t.Fatalf("lastMessageID = %q, want assistant", got)
	}
}

// TestRunTailKeepsEarlierStatusBlocks 回归：新会话先 /status 再发消息时，状态块不得被尾部替换删除
func TestRunTailKeepsEarlierStatusBlocks(t *testing.T) {
	m := testModel(120, 30)
	m.handlers.Request = func(Request) tea.Cmd { return nil }
	m.Update(statusReport())
	if got := m.lastMessageID(); got != "" {
		t.Fatalf("lastMessageID = %q, want empty before any conversation", got)
	}
	// 首条消息以空 AfterID 完成，尾部替换从索引 0 开始
	m.Update(OperationDoneMsg{RetainMessages: true, Tail: &MessageTail{Messages: []Message{{ID: "user-1", Role: "user", Text: "hello"}}}})
	if len(m.state.Messages) != 3 {
		t.Fatalf("messages = %+v, want status pair followed by the user turn", m.state.Messages)
	}
	if m.state.Messages[0].Role != "command" || m.state.Messages[1].Role != "status" || m.state.Messages[2].ID != "user-1" {
		t.Fatalf("order = %+v, want status pair then user turn", m.state.Messages)
	}
}

// TestRunTailKeepsStatusBlocksInterleavedWithHistory 验证状态块夹在历史中时原位保留
func TestRunTailKeepsStatusBlocksInterleavedWithHistory(t *testing.T) {
	m := testModel(120, 30)
	m.handlers.Request = func(Request) tea.Cmd { return nil }
	m.Update(OperationDoneMsg{RetainMessages: true, Tail: &MessageTail{Messages: []Message{{ID: "user-1", Role: "user", Text: "first"}}}})
	m.Update(statusReport())
	m.Update(OperationDoneMsg{RetainMessages: true, Tail: &MessageTail{AfterID: "user-1", Messages: []Message{{ID: "user-2", Role: "user", Text: "second"}}}})
	want := []string{"user-1", "status-command/1", "status/1", "user-2"}
	if len(m.state.Messages) != len(want) {
		t.Fatalf("messages = %+v", m.state.Messages)
	}
	for i, id := range want {
		if m.state.Messages[i].ID != id {
			t.Fatalf("messages[%d] = %q, want %q (all: %+v)", i, m.state.Messages[i].ID, id, m.state.Messages)
		}
	}
}
