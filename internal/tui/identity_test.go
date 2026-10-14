package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCommittedIdentityReplacesLiveBlockWithoutDuplicates(t *testing.T) {
	m := New(Snapshot{Messages: []Message{{ID: "live", Role: "assistant", Text: "partial"}}}, Handlers{})
	m.Update(MessageCommittedMsg{PreviousID: "live", Message: Message{ID: "source", Role: "assistant", Text: "final"}})
	m.Update(MessageMsg{ID: "source", Role: "assistant", Text: "updated"})
	if len(m.state.Messages) != 1 || m.state.Messages[0].Text != "updated" {
		t.Fatalf("identity replacement = %+v", m.state.Messages)
	}
}

func TestStaleCompletionCannotClearNewOperation(t *testing.T) {
	m := New(Snapshot{}, Handlers{Request: func(Request) tea.Cmd { return nil }})
	m.dispatch("run", "hello")
	m.Update(OperationDoneMsg{ID: m.operationID + 1})
	if !m.busy {
		t.Fatal("stale completion cleared active operation")
	}
	m.Update(OperationDoneMsg{ID: m.operationID})
	if m.busy {
		t.Fatal("current completion retained busy state")
	}
}

func TestOperationTailKeepsHistoryAndNoticeIdentity(t *testing.T) {
	m := New(Snapshot{Messages: []Message{{ID: "history", Text: "old"}, {ID: "notice", Text: "notice"}, {ID: "live", Text: "partial"}}}, Handlers{})
	m.Update(OperationDoneMsg{RetainMessages: true, Tail: &MessageTail{AfterID: "notice", Messages: []Message{{ID: "source", Text: "final"}}}})
	if len(m.state.Messages) != 3 || m.state.Messages[0].ID != "history" || m.state.Messages[1].ID != "notice" || m.state.Messages[2].ID != "source" {
		t.Fatalf("tail replacement = %+v", m.state.Messages)
	}
}
