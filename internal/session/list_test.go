package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// newSessionFile 在 dir 下创建一个已落盘的会话，返回文件路径；userText 为首条用户消息内容
func newSessionFile(t *testing.T, dir, cwd, userText string) string {
	t.Helper()
	m := New(cwd, dir)
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: userText}}}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	return m.sessionFile
}

func TestListSessionsEmptyWhenDirMissing(t *testing.T) {
	infos, err := listSessionsInDir(filepath.Join(t.TempDir(), "not-exist"))
	if err != nil {
		t.Fatalf("listSessionsInDir() error = %v, want nil", err)
	}
	if len(infos) != 0 {
		t.Fatalf("listSessionsInDir() = %d sessions, want 0", len(infos))
	}
}

func TestListSessionsSortedByModTimeDesc(t *testing.T) {
	dir := t.TempDir()
	newSessionFile(t, dir, "/tmp/proj", "old session")
	time.Sleep(10 * time.Millisecond)
	newSessionFile(t, dir, "/tmp/proj", "new session")

	infos, err := listSessionsInDir(dir)
	if err != nil {
		t.Fatalf("listSessionsInDir() error = %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("listSessionsInDir() = %d sessions, want 2", len(infos))
	}
	if !strings.Contains(infos[0].Summary, "new session") {
		t.Errorf("first summary = %q, want the newest session", infos[0].Summary)
	}
	if !infos[0].ModTime.After(infos[1].ModTime) {
		t.Errorf("mod times not descending: %v <= %v", infos[0].ModTime, infos[1].ModTime)
	}
}

func TestListSessionsIgnoresNonJSONL(t *testing.T) {
	dir := t.TempDir()
	newSessionFile(t, dir, "/tmp/proj", "hello")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	infos, err := listSessionsInDir(dir)
	if err != nil {
		t.Fatalf("listSessionsInDir() error = %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("listSessionsInDir() = %d sessions, want 1", len(infos))
	}
}

func TestListSessionsTruncatesSummary(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("长", summaryMaxRunes+10)
	newSessionFile(t, dir, "/tmp/proj", long)

	infos, err := listSessionsInDir(dir)
	if err != nil {
		t.Fatalf("listSessionsInDir() error = %v", err)
	}
	want := strings.Repeat("长", summaryMaxRunes) + "..."
	if infos[0].Summary != want {
		t.Errorf("summary len = %d runes, want %d runes with ellipsis", len([]rune(infos[0].Summary)), len([]rune(want)))
	}
}

func TestListSessionsEmptyFileHasEmptySummary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026_empty_session.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	infos, err := listSessionsInDir(dir)
	if err != nil {
		t.Fatalf("listSessionsInDir() error = %v", err)
	}
	if len(infos) != 1 || infos[0].Summary != "" {
		t.Fatalf("listSessionsInDir() = %+v, want one session with empty summary", infos)
	}
}

func TestListSessionsUsesDefaultDir(t *testing.T) {
	// DefaultDir 以用户主目录为根，覆盖 HOME 后验证导出入口走同一目录约定
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	dir, err := DefaultDir(cwd)
	if err != nil {
		t.Fatalf("DefaultDir() error = %v", err)
	}
	newSessionFile(t, dir, cwd, "entry point")

	infos, err := ListSessions(cwd)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(infos) != 1 || !strings.Contains(infos[0].Summary, "entry point") {
		t.Fatalf("ListSessions() = %+v, want the session under DefaultDir", infos)
	}
}
