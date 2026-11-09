package tui

import (
	"errors"
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
	if len(requests) != 1 || !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "usage: /rename") {
		t.Fatal("empty rename did not show usage")
	}
}

func TestNewSessionResetsUIOnlyAfterSuccess(t *testing.T) {
	m := testModel(120, 30)
	m.rememberHistory(ComposerHistoryEntry{Draft: ComposerDraft{Text: "old input"}})
	m.editor.SetValue("old draft")
	m.transcript.showThinking, m.transcript.showAllTools = true, true
	m.state.Messages = []Message{{ID: "old", Role: "user", Text: "old conversation"}}
	m.Update(OperationDoneMsg{Notice: "New session started", Snapshot: &Snapshot{Thinking: "high"}, ResetSessionUI: true})
	if m.editor.Value() != "" || m.transcript.showThinking || m.transcript.showAllTools || m.state.Thinking != "high" {
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
	for _, input := range []string{"/new", "/rename name", "/reload", "/clear", "/delete"} {
		m.editor.SetValue(input)
		m.submit()
	}
	if len(requests) != 0 || m.editor.Value() != "/delete" {
		t.Fatal("busy state dispatched a command or cleared input")
	}
}

func TestClearAndDeleteCommands(t *testing.T) {
	m := testModel(120, 30)
	var requests []Request
	m.handlers.Request = func(r Request) tea.Cmd { requests = append(requests, r); return nil }
	m.state.Messages = []Message{{ID: "old", Role: "user", Text: "old context"}}
	m.rememberHistory(ComposerHistoryEntry{Draft: ComposerDraft{Text: "previous"}})
	m.editor.SetValue("/clear")
	m.submit()
	if len(requests) != 1 || requests[0].Kind != "clear" {
		t.Fatalf("clear request = %+v", requests)
	}
	m.Update(OperationDoneMsg{ID: m.operationID, Snapshot: &Snapshot{}, ResetTranscript: true})
	if len(m.state.Messages) != 0 || m.editor.Value() != "" {
		t.Fatal("clear did not only reset the transcript")
	}
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "previous")

	m.editor.SetValue("/delete extra")
	m.submit()
	if len(requests) != 1 || !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "Usage: /delete") {
		t.Fatal("delete accepted an argument")
	}
	m.editor.SetValue("/delete")
	m.submit()
	if len(requests) != 2 || requests[1].Kind != "delete" {
		t.Fatalf("delete request = %+v", requests)
	}
	_, quit := m.Update(OperationDoneMsg{ID: m.operationID, Quit: true})
	if !m.closing || quit == nil {
		t.Fatal("delete did not quit the TUI")
	}

	m = testModel(120, 30)
	m.operationID = 1
	_, cmd := m.Update(OperationDoneMsg{ID: 1, Quit: true, Err: errors.New("delete failed")})
	if m.closing || cmd != nil || len(m.state.Messages) == 0 || !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "delete failed") {
		t.Fatal("delete failure incorrectly quit the TUI")
	}
}
