package session

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestTreeSnapshotAndRewindUser(t *testing.T) {
	m := New("/project", t.TempDir())
	messages := []llm.Message{
		userMsg("first"),
		assistantMsg("answer"),
		llm.ToolResultMessage{ToolCallID: "call", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "tool"}}},
		userMsg("second\nline"),
		assistantMsg("follow-up"),
	}
	for _, message := range messages {
		if err := m.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	u1, assistant, tool, u2 := m.entries[0].base().ID, m.entries[1].base().ID, m.entries[2].base().ID, m.entries[3].base().ID
	snapshot := m.Tree()
	if snapshot.LeafID != m.leafID || len(snapshot.Entries) != len(messages) {
		t.Fatalf("snapshot = %+v, want %d entries at leaf %q", snapshot, len(messages), m.leafID)
	}
	if snapshot.Entries[3].ParentID != tool || snapshot.Entries[3].Role != "user" || snapshot.Entries[3].Text != "second\nline" {
		t.Fatalf("user tree entry = %+v", snapshot.Entries[3])
	}
	if snapshot.Entries[1].Role != "assistant" || snapshot.Entries[2].Role != "toolResult" {
		t.Fatalf("hidden roles = %q/%q", snapshot.Entries[1].Role, snapshot.Entries[2].Role)
	}
	snapshot.Entries[0].Text = "mutated"
	if got := m.Tree().Entries[0].Text; got != "first" {
		t.Fatalf("tree snapshot leaked mutable entry: %q", got)
	}

	beforeLines := len(readLines(t, m.sessionFile))
	rewound, err := m.RewindUser(u2)
	if err != nil {
		t.Fatal(err)
	}
	if rewound.PreviousLeafID != m.entries[4].base().ID || rewound.ParentID != tool || rewound.Text != "second\nline" || m.LeafID() != tool {
		t.Fatalf("rewind = %+v, leaf = %q", rewound, m.LeafID())
	}
	if got := len(readLines(t, m.sessionFile)); got != beforeLines {
		t.Fatalf("rewind changed persisted lines: got %d want %d", got, beforeLines)
	}
	reopened, err := Open(m.sessionFile, "/project")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.LeafID() != rewound.PreviousLeafID {
		t.Fatalf("reopened leaf = %q, want last persisted entry %q", reopened.LeafID(), rewound.PreviousLeafID)
	}
	if err := m.AppendMessage(userMsg("new branch")); err != nil {
		t.Fatal(err)
	}
	if parent := m.entries[len(m.entries)-1].base().ParentID; parent == nil || *parent != tool {
		t.Fatalf("new branch parent = %v, want hidden tool %q", parent, tool)
	}

	if _, err := m.RewindUser(assistant); err == nil {
		t.Fatal("assistant rewind succeeded")
	}
	if _, err := m.RewindUser("missing"); err == nil {
		t.Fatal("unknown user rewind succeeded")
	}
	if err := m.Branch(u1); err != nil {
		t.Fatal(err)
	}
	rewound, err = m.RewindUser(u1)
	if err != nil || rewound.ParentID != "" || m.LeafID() != "" {
		t.Fatalf("root rewind = %+v, leaf = %q, err = %v", rewound, m.LeafID(), err)
	}
}
