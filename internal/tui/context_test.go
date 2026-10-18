package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestFormatContextUsage(t *testing.T) {
	if got := formatContextUsage(ContextUsageMsg{Tokens: 1234, ContextWindow: 200000, Available: true}); got != "context: 1.2k/200.0k" {
		t.Fatalf("formatContextUsage = %q", got)
	}
	if got := formatContextUsage(ContextUsageMsg{Tokens: 0, ContextWindow: 999, Available: true}); got != "context: 0.0/999.0" {
		t.Fatalf("formatContextUsage zero = %q", got)
	}
	if got := formatContextUsage(ContextUsageMsg{}); got != "context: unavailable" {
		t.Fatalf("formatContextUsage unavailable = %q", got)
	}
}

func TestContextCommandAppendsWhiteRuntimeLine(t *testing.T) {
	m := testModel(100, 24)
	m.handlers.Request = func(Request) tea.Cmd { return nil }
	called := false
	m.handlers.Context = func() tea.Cmd {
		called = true
		return func() tea.Msg {
			return ContextUsageMsg{Tokens: 1234, ContextWindow: 200000, Available: true}
		}
	}
	m.editor.SetValue("/context")
	cmd := m.submit()
	if !called || cmd == nil {
		t.Fatalf("context command called=%v cmd=%v", called, cmd != nil)
	}
	if msg := cmd(); msg == nil {
		t.Fatal("context command returned nil message")
	} else {
		m.Update(msg)
	}
	last := m.state.Messages[len(m.state.Messages)-1]
	if last.Role != "context" || last.Text != "context: 1.2k/200.0k" {
		t.Fatalf("context line = %+v", last)
	}
	rendered := m.renderMessage(last)
	if ansi.Strip(rendered) != last.Text || strings.Contains(rendered, "999") {
		t.Fatalf("context render = %q", rendered)
	}
}

func TestContextCommandRejectsBusyAndArguments(t *testing.T) {
	m := testModel(100, 24)
	called := false
	m.handlers.Context = func() tea.Cmd {
		called = true
		return nil
	}
	m.busy = true
	m.editor.SetValue("/context")
	m.submit()
	if called || len(m.state.Messages) == 0 || !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "Commands are unavailable") {
		t.Fatalf("busy context command called=%v messages=%+v", called, m.state.Messages)
	}

	m = testModel(100, 24)
	m.handlers.Request = func(Request) tea.Cmd { return nil }
	m.handlers.Context = func() tea.Cmd { return nil }
	m.editor.SetValue("/context extra")
	m.submit()
	if len(m.state.Messages) == 0 || m.state.Messages[len(m.state.Messages)-1].Text != "Usage: /context" {
		t.Fatalf("argument error = %+v", m.state.Messages)
	}
}

func TestLastMessageIDSkipsRuntimeOnlyRows(t *testing.T) {
	m := testModel(100, 24)
	m.state.Messages = []Message{
		{ID: "assistant", Role: "assistant", Text: "answer"},
		{ID: "context", Role: "context", Text: "context: 1.2k/2.0k"},
	}
	if got := m.lastMessageID(); got != "assistant" {
		t.Fatalf("lastMessageID = %q, want assistant", got)
	}
}
