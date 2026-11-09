package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 子任务把任务号直接作为会话号,不再额外生成随机标识
func TestNewAtUsesExplicitSessionID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-1.jsonl")
	sm, err := NewAt("/project", path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if sm.ID() != "agent-1" {
		t.Fatalf("session id = %q, want task id", sm.ID())
	}
	if sm.Path() != path {
		t.Fatalf("session path = %q, want %q", sm.Path(), path)
	}
	// 任务号直接参与文件名,非法值必须在建会话前拒绝
	if _, err := NewAt("/project", path, "../escape"); err == nil {
		t.Fatal("invalid session id was accepted")
	}
}

// 带身份校验的打开入口必须拒绝空白、缺头与错配,并且不改动文件
func TestOpenWithIDRejectsInvalidIdentityWithoutMutation(t *testing.T) {
	const want = "agent-1"
	cases := []struct {
		name    string
		content string
		write   bool
	}{
		{name: "missing file"},
		{name: "empty file", content: "", write: true},
		{name: "whitespace only", content: "\n \n", write: true},
		{name: "missing header", content: `{"type":"message","id":"m1","parentId":null,"timestamp":"t"}`, write: true},
		{name: "foreign id", content: `{"type":"session","version":1,"id":"other-task","timestamp":"t","cwd":"/project"}` + "\n", write: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), want+".jsonl")
			if testCase.write {
				if err := os.WriteFile(path, []byte(testCase.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if _, err := OpenWithID(path, want); err == nil {
				t.Fatal("invalid identity was accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("session file mutated: before %q after %q", before, after)
			}
		})
	}
}

// 合法记录重开时保留会话号与历史
func TestOpenWithIDKeepsMatchingSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-1.jsonl")
	sm, err := NewAt("/project", path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithID(path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID() != "agent-1" {
		t.Fatalf("reopened id = %q, want agent-1", reopened.ID())
	}
	if got := llm.TextOf(reopened.BuildContext().Messages()[0].(llm.UserMessage).Content); got != "hello" {
		t.Fatalf("reopened context = %q, want hello", got)
	}
}

// 文件备份目录跟随会话号;子会话号等于任务号,所以续跑与重开都写回同一目录
func TestFileHistoryBackupsFollowSessionIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-1.jsonl")
	root := t.TempDir()
	tracked := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	sm, err := NewAt("/project", path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	sm.AttachFileHistory(root)
	if err := sm.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}); err != nil {
		t.Fatal(err)
	}
	firstID := sm.LeafID()
	if err := sm.MakeFileHistorySnapshot(firstID); err != nil {
		t.Fatal(err)
	}
	if err := sm.FileHistory().TrackEdit(context.Background(), firstID, tracked); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "file-history", "agent-1")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("backup dir %q: %v", backupDir, err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].Name(), "@v1") {
		t.Fatalf("backups = %v, want one v1 backup", entries)
	}

	// 重开同一任务号:快照仍在,新备份继续写同一目录
	reopened, err := OpenWithID(path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	reopened.AttachFileHistory(root)
	if got := len(reopened.FileHistory().Snapshots()); got != 1 {
		t.Fatalf("snapshots after reopen = %d, want 1", got)
	}
	if err := reopened.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	secondID := reopened.LeafID()
	// 新 turn 开始时文件已是上一轮改过的内容,快照据此生成下一个版本
	if err := os.WriteFile(tracked, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reopened.MakeFileHistorySnapshot(secondID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.FileHistory().TrackEdit(context.Background(), secondID, tracked); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	foundV2 := false
	for _, entry := range entries {
		foundV2 = foundV2 || strings.Contains(entry.Name(), "@v2")
	}
	if !foundV2 {
		t.Fatalf("backups after resume = %v, want a v2 backup in the same dir", entries)
	}
}
