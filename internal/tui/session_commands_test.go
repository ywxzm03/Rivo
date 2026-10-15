package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRenameCommandRequiresNameAndKeepsSpaces(t *testing.T) {
	m := testModel(120, 30)
	var requests []Request
	m.handlers.Request = func(r Request) tea.Cmd { requests = append(requests, r); return nil }
	m.editor.SetValue("/rename  My new Thread  ")
	m.submit()
	if len(requests) != 1 || requests[0].Kind != "rename" || requests[0].Value != "My new Thread" {
		t.Fatalf("requests = %+v", requests)
	}
	m.Update(OperationDoneMsg{})
	m.editor.SetValue("/rename")
	m.submit()
	if len(requests) != 1 || !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "Usage: /rename") {
		t.Fatal("empty rename did not show usage")
	}
}

func TestNewSessionResetsUIOnlyAfterSuccess(t *testing.T) {
	m := testModel(120, 30)
	m.history, m.historyPos, m.draft = []string{"old input"}, 1, "old draft"
	m.hideThinking, m.showAllTools = true, true
	m.state.Messages = []Message{{ID: "old", Role: "user", Text: "old conversation"}}
	m.Update(OperationDoneMsg{Notice: "New session started", Snapshot: &Snapshot{Thinking: "high"}, ResetSessionUI: true})
	if len(m.history) != 0 || m.draft != "" || m.editor.Value() != "" || m.hideThinking || m.showAllTools || m.state.Thinking != "high" {
		t.Fatal("new session did not restore initial UI state")
	}
	if len(m.state.Messages) != 1 || m.state.Messages[0].Text != "New session started" {
		t.Fatal("new session kept old transcript")
	}
	press(m, tea.KeyUp)
	if m.editor.Value() != "" {
		t.Fatal("old input history survived new session")
	}
}

func TestSessionCommandsRejectArgumentsAndBusyState(t *testing.T) {
	m := testModel(120, 30)
	var requests []Request
	m.handlers.Request = func(r Request) tea.Cmd { requests = append(requests, r); return nil }
	for _, input := range []string{"/new extra", "/reload extra"} {
		m.editor.SetValue(input)
		m.submit()
	}
	if len(requests) != 0 {
		t.Fatal("argument-free commands accepted arguments")
	}
	m.busy = true
	for _, input := range []string{"/new", "/rename name", "/reload"} {
		m.editor.SetValue(input)
		m.submit()
	}
	if len(requests) != 0 || m.editor.Value() != "/reload" {
		t.Fatal("busy state dispatched a command or cleared input")
	}
}
