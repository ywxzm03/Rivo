package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestFileHistoryPruneKeepsRewindTarget(t *testing.T) {
	m := New("/project", t.TempDir())
	m.AttachFileHistory(t.TempDir())
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}); err != nil {
		t.Fatal(err)
	}
	first := m.LeafID()
	if err := m.MakeFileHistorySnapshot(first); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "answer"}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	second := m.LeafID()
	if err := m.MakeFileHistorySnapshot(second); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RewindUser(second); err != nil {
		t.Fatal(err)
	}
	m.PruneFileHistoryToActiveBranch(second)
	var found bool
	for _, snapshot := range m.FileHistory().Snapshots() {
		found = found || snapshot.MessageID == second
	}
	if !found {
		t.Fatalf("snapshots = %+v, want rewind target %q retained", m.FileHistory().Snapshots(), second)
	}
}

func TestFileHistoryBranchPrunesInactiveSnapshots(t *testing.T) {
	m := New("/project", t.TempDir())
	m.AttachFileHistory(t.TempDir())
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}); err != nil {
		t.Fatal(err)
	}
	first := m.LeafID()
	if err := m.MakeFileHistorySnapshot(first); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	second := m.LeafID()
	if err := m.MakeFileHistorySnapshot(second); err != nil {
		t.Fatal(err)
	}
	if err := m.Branch(first); err != nil {
		t.Fatal(err)
	}
	snapshots := m.FileHistory().Snapshots()
	if len(snapshots) != 1 || snapshots[0].MessageID != first {
		t.Fatalf("snapshots after branch = %+v, want only %q", snapshots, first)
	}
}

func TestAttachFileHistoryPrunesInactivePersistedBranch(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	m := New("/project", dir)
	m.AttachFileHistory(root)
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}); err != nil {
		t.Fatal(err)
	}
	first := m.LeafID()
	if err := m.MakeFileHistorySnapshot(first); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	second := m.LeafID()
	if err := m.MakeFileHistorySnapshot(second); err != nil {
		t.Fatal(err)
	}
	if err := m.Branch(first); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "third"}}}); err != nil {
		t.Fatal(err)
	}
	third := m.LeafID()
	if err := m.MakeFileHistorySnapshot(third); err != nil {
		t.Fatal(err)
	}
	loaded, err := Open(m.Path(), "/project")
	if err != nil {
		t.Fatal(err)
	}
	loaded.AttachFileHistory(root)
	var ids []string
	for _, snapshot := range loaded.FileHistory().Snapshots() {
		ids = append(ids, snapshot.MessageID)
	}
	if contains(ids, second) {
		t.Fatalf("loaded snapshots = %v, want inactive branch %q pruned", ids, second)
	}
	if !contains(ids, first) || !contains(ids, third) {
		t.Fatalf("loaded snapshots = %v, want active branch %q and %q", ids, first, third)
	}
}

func TestOpenSkipsMalformedFileHistoryRecordAndRecordsWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	header, err := json.Marshal(Header{Type: TypeSession, Version: CurrentVersion, ID: "s1", Timestamp: "t", Cwd: "/project"})
	if err != nil {
		t.Fatal(err)
	}
	data := append(header, '\n')
	data = append(data, []byte(`{"type":"file-history-snapshot","snapshot":{}}`+"\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("Open() error = %v, want malformed metadata to be skipped", err)
	}
	warnings := loaded.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped invalid file history record") {
		t.Fatalf("warnings = %v, want one file history warning", warnings)
	}
}

func TestWarningsAreBoundedAndDeduplicated(t *testing.T) {
	m := New("/project", t.TempDir())
	for index := 0; index < maxSessionWarnings+10; index++ {
		m.AddWarning(fmt.Sprintf("warning-%d", index))
	}
	m.AddWarning("warning-109")
	warnings := m.Warnings()
	if len(warnings) != maxSessionWarnings {
		t.Fatalf("warnings = %d, want %d", len(warnings), maxSessionWarnings)
	}
	if warnings[0] != "warning-10" || warnings[len(warnings)-1] != "warning-109" {
		t.Fatalf("warning window = %q ... %q, want warning-10 ... warning-109", warnings[0], warnings[len(warnings)-1])
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
