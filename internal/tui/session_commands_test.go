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
