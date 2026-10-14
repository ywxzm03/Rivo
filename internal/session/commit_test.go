package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestPendingBatchKeepsIDsAndBlocksNewSubmissions(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := New("/project", filepath.Join(blocker, "sessions"))
	if _, err := m.AppendMessages([]llm.Message{userMsg("hello"), assistantMsg("answer")}); err == nil {
		t.Fatal("want storage failure")
	}
	ids := []string{m.pending[0].base().ID, m.pending[1].base().ID}
	if len(m.entries) != 0 || m.leafID != "" || len(m.byID) != 0 {
		t.Fatal("uncommitted batch entered authoritative state")
	}
	if err := m.AppendMessage(userMsg("new")); err == nil || len(m.pending) != 2 {
		t.Fatal("new submission replaced pending results")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryPending(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{m.entries[0].base().ID, m.entries[1].base().ID}) {
		t.Fatal("retry generated different entry IDs")
	}
	if *m.entries[1].base().ParentID != ids[0] {
		t.Fatal("batch order broke parent chain")
	}
	if err := m.RetryPending(); err != nil {
		t.Fatal(err)
	}
	if len(readLines(t, m.Path())) != 3 {
		t.Fatal("retry duplicated disk records")
	}
}

func TestSetupPersistsBeforePublishWithoutListingEmptySession(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	if err := m.AppendThinkingLevelChange("high"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Path() + ".draft"); err != nil {
		t.Fatal("setting was published without disk storage", err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")); len(files) != 0 {
		t.Fatal("setting created a visible empty session")
	}
	if err := m.AppendMessage(userMsg("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Path() + ".draft"); !os.IsNotExist(err) {
		t.Fatal("draft remained after session publication")
	}
}
