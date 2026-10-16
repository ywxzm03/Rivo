package adapter

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/session"
)

func TestProjectTreeShowsOnlyUsersAndFoldsHiddenParents(t *testing.T) {
	snapshot := session.TreeSnapshot{
		LeafID: "assistant-2",
		Entries: []session.TreeEntry{
			{ID: "user-1", Role: "user", Text: "first"},
			{ID: "assistant-1", ParentID: "user-1", Role: "assistant", Text: "answer"},
			{ID: "tool-1", ParentID: "assistant-1", Role: "toolResult", Text: "tool result"},
			{ID: "user-2", ParentID: "tool-1", Role: "user", Text: "second\nline"},
			{ID: "assistant-2", ParentID: "user-2", Role: "assistant", Text: "follow-up"},
			{ID: "user-3", ParentID: "assistant-1", Role: "user", Text: "branch"},
		},
	}

	nodes := projectTree(snapshot)
	if len(nodes) != 3 {
		t.Fatalf("nodes = %+v, want only three users", nodes)
	}
	if nodes[0].ID != "user-1" || nodes[0].ParentID != "" || nodes[0].Prefix != "" {
		t.Fatalf("root node = %+v", nodes[0])
	}
	if nodes[1].ID != "user-2" || nodes[1].ParentID != "user-1" || nodes[1].Prefix != "├─ " || nodes[1].Text != "second line" || !nodes[1].Current {
		t.Fatalf("active child node = %+v", nodes[1])
	}
	if nodes[2].ID != "user-3" || nodes[2].ParentID != "user-1" || nodes[2].Prefix != "└─ " || nodes[2].Current {
		t.Fatalf("branch node = %+v", nodes[2])
	}
	for _, node := range nodes {
		if node.Text == "answer" || node.Text == "tool result" || node.Text == "follow-up" {
			t.Fatalf("hidden message leaked into tree: %+v", node)
		}
	}
}
