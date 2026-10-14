package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSessionNamePersistsAcrossBranchesAndCompaction(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(userMsg("first message")); err != nil {
		t.Fatal(err)
	}
	branch := m.LastEntryID()
	before := m.BuildContext().Messages()
	if err := m.AppendSessionInfo("  First\r\nThread  "); err != nil {
		t.Fatal(err)
	}
	if m.Name() != "First Thread" || !reflect.DeepEqual(before, m.BuildContext().Messages()) {
		t.Fatal("rename changed context or did not normalize the name")
	}
	if err := m.Branch(branch); err != nil {
		t.Fatal(err)
	}
	if m.Name() != "First Thread" {
		t.Fatal("branching changed the Thread name")
	}
	if err := m.AppendSessionInfo("Second Thread"); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendCompaction("summary", "", 100, nil, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := Open(m.Path(), "/project")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name() != "Second Thread" || len(loaded.BuildContext().Messages()) != 1 {
		t.Fatal("name or compacted context was lost on resume")
	}
	infos, err := listSessionsInDir(filepath.Dir(m.Path()))
	if err != nil || len(infos) != 1 || infos[0].Name != "Second Thread" || infos[0].Summary != "first message" {
		t.Fatalf("session list = %+v, err = %v", infos, err)
	}
}

func TestNamedEmptySessionIsPublished(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendThinkingLevelChange("high"); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendSessionInfo("\r\n "); err == nil || m.Name() != "" || len(m.entries) != 1 {
		t.Fatal("empty rename submitted a record")
	}
	if err := m.AppendSessionInfo("Named empty thread"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Path() + ".draft"); !os.IsNotExist(err) {
		t.Fatal("named session retained the draft")
	}
	loaded, err := Open(m.Path(), "/project")
	if err != nil || loaded.Name() != "Named empty thread" || len(loaded.BuildContext().Messages()) != 0 || loaded.BuildContext().ThinkingLevel != "high" {
		t.Fatalf("loaded named session = %+v, err = %v", loaded, err)
	}
	infos, err := listSessionsInDir(filepath.Dir(m.Path()))
	if err != nil || len(infos) != 1 || infos[0].Name != "Named empty thread" || infos[0].Summary != "" {
		t.Fatalf("named empty session list = %+v, err = %v", infos, err)
	}
}

func TestFailedRenameKeepsCommittedNameAndRetriesOnce(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := New("/project", filepath.Join(blocker, "sessions"))
	if err := m.AppendSessionInfo("Pending name"); err == nil || m.Name() != "" {
		t.Fatal("failed rename published its name")
	}
	id := m.pending[0].base().ID
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryPending(); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryPending(); err != nil {
		t.Fatal(err)
	}
	if m.Name() != "Pending name" || len(m.entries) != 1 || m.LastEntryID() != id || len(readLines(t, m.Path())) != 2 {
		t.Fatal("retry lost the name or duplicated the metadata")
	}
}
