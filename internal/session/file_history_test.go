package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestFileHistoryMetadataDoesNotAdvanceTreeLeaf(t *testing.T) {
	root := t.TempDir()
	m := New("/project", root)
	m.AttachFileHistory(t.TempDir())
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "user"}}}); err != nil {
		t.Fatal(err)
	}
	userID := m.LeafID()
	if err := m.MakeFileHistorySnapshot(userID); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.FileHistory().TrackEdit(context.Background(), userID, tracked); err != nil {
		t.Fatal(err)
	}
	if got := m.LeafID(); got != userID {
		t.Fatalf("leaf after metadata = %q, want %q", got, userID)
	}
	if err := m.AppendMessage(llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "assistant"}}}); err != nil {
		t.Fatal(err)
	}
	entries := m.Tree().Entries
	if len(entries) != 2 || entries[1].ParentID != userID {
		t.Fatalf("tree entries = %+v, want assistant parent %q", entries, userID)
	}

	data, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, line := range splitLines(data) {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &probe); err == nil {
			types = append(types, probe.Type)
		}
	}
	if !contains(types, TypeFileHistorySnapshot) {
		t.Fatalf("JSONL types = %v, want file history snapshot", types)
	}
	if !contains(types, TypeFileHistoryDelta) {
		t.Fatalf("JSONL types = %v, want file history delta", types)
	}

	loaded, err := Open(m.Path(), "/project")
	if err != nil {
		t.Fatal(err)
	}
	loaded.AttachFileHistory(t.TempDir())
	if len(loaded.FileHistory().Snapshots()) != 1 {
		t.Fatalf("loaded snapshots = %d, want 1", len(loaded.FileHistory().Snapshots()))
	}
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for index, value := range data {
		if value != '\n' {
			continue
		}
		if index > start {
			lines = append(lines, data[start:index])
		}
		start = index + 1
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
