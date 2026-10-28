package session

import (
	"reflect"
	"testing"

	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestCompactionDetailsRoundTripAndContextProjection(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(userMsg("before")); err != nil {
		t.Fatal(err)
	}
	details := &ctxview.FileLists{
		ReadFiles:     []string{"README.md"},
		ModifiedFiles: []string{"main.go"},
	}
	if err := m.AppendCompactionWithDetails("summary", m.leafID, 12, nil, nil, details); err != nil {
		t.Fatal(err)
	}

	// The entry owns a snapshot instead of the caller's mutable slices.
	details.ReadFiles[0] = "changed.md"
	entry := m.entries[1].(*CompactionEntry)
	want := &ctxview.FileLists{ReadFiles: []string{"README.md"}, ModifiedFiles: []string{"main.go"}}
	if !reflect.DeepEqual(entry.Details, want) {
		t.Fatalf("entry details = %+v, want %+v", entry.Details, want)
	}

	ctx := m.BuildContext()
	if len(ctx.Messages()) != 2 {
		t.Fatalf("context messages = %d, want 2", len(ctx.Messages()))
	}
	if len(ctx.Entries) != 2 || !reflect.DeepEqual(ctx.Entries[0].Details, want) {
		t.Fatalf("context compaction details = %+v, want %+v", ctx.Entries[0].Details, want)
	}
	ctx.Entries[0].Details.ReadFiles[0] = "context-only.md"
	if !reflect.DeepEqual(entry.Details, want) {
		t.Fatalf("entry details changed through context projection: %+v", entry.Details)
	}

	reopened, err := Open(m.Path(), "/project")
	if err != nil {
		t.Fatal(err)
	}
	reopenedEntry := reopened.entries[1].(*CompactionEntry)
	if !reflect.DeepEqual(reopenedEntry.Details, want) {
		t.Fatalf("reopened details = %+v, want %+v", reopenedEntry.Details, want)
	}
}

func TestCompactionWithoutDetailsDecodesAsNil(t *testing.T) {
	entry, err := decodeEntry(TypeCompaction, `{"type":"compaction","id":"c1","parentId":null,"timestamp":"2026-01-01T00:00:00Z","summary":"summary","firstKeptEntryId":"c1","tokensBefore":1}`)
	if err != nil {
		t.Fatal(err)
	}
	compaction, ok := entry.(*CompactionEntry)
	if !ok {
		t.Fatalf("entry type = %T, want *CompactionEntry", entry)
	}
	if compaction.Details != nil {
		t.Fatalf("details = %+v, want nil for old record", compaction.Details)
	}
}

func TestAppendCompactionUsesFirstNonNilDetails(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "before"}}}); err != nil {
		t.Fatal(err)
	}
	want := &ctxview.FileLists{ReadFiles: []string{}, ModifiedFiles: []string{"main.go"}}
	if err := m.AppendCompactionWithDetails("summary", m.leafID, 1, nil, nil, want); err != nil {
		t.Fatal(err)
	}
	got := m.entries[1].(*CompactionEntry).Details
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("details = %+v, want %+v", got, want)
	}
}
