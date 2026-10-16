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

func TestNamedEmptySessionStaysInMemory(t *testing.T) {
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
	if m.Name() != "Named empty thread" {
		t.Fatal("name was not committed in memory")
	}
	if _, err := os.Stat(m.Path()); !os.IsNotExist(err) {
		t.Fatal("named empty session was published")
	}
	if _, err := os.Stat(m.Path() + ".draft"); !os.IsNotExist(err) {
		t.Fatal("named empty session created a draft")
	}
	if infos, err := listSessionsInDir(filepath.Dir(m.Path())); err != nil || len(infos) != 0 {
		t.Fatalf("named empty session list = %+v, err = %v", infos, err)
	}
	if err := m.AppendMessage(userMsg("first message")); err != nil {
		t.Fatal(err)
	}
	loaded, err := Open(m.Path(), "/project")
	if err != nil || loaded.Name() != "Named empty thread" || len(loaded.BuildContext().Messages()) != 1 || loaded.BuildContext().ThinkingLevel != "high" {
		t.Fatalf("loaded named session = %+v, err = %v", loaded, err)
	}
}

func TestFailedRenameKeepsCommittedNameAndRetriesOnce(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	if err := m.AppendMessage(userMsg("first message")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendSessionInfo("Committed name"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(m.Path(), m.Path()+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(m.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendSessionInfo("Pending name"); err == nil || m.Name() != "Committed name" {
		t.Fatal("failed rename changed the committed name")
	}
	id := m.pending[0].base().ID
	if err := os.Remove(m.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(m.Path()+".saved", m.Path()); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryPending(); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryPending(); err != nil {
		t.Fatal(err)
	}
	if m.Name() != "Pending name" || len(m.entries) != 3 || m.LastEntryID() != id || len(readLines(t, m.Path())) != 4 {
		t.Fatal("retry lost the name or duplicated the metadata")
	}
}
