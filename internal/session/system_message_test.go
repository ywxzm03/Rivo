package session

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func systemMsg(sections map[string]string) llm.SystemMessage {
	return llm.SystemMessage{Sections: sections}
}

// TestSystemMessageProjectionInPlace 无压缩时 system 条目按树序就地进入投影
func TestSystemMessageProjectionInPlace(t *testing.T) {
	m := New("/project", t.TempDir())
	for _, msg := range []llm.Message{
		systemMsg(map[string]string{"preamble": "base"}),
		userMsg("u1"),
		systemMsg(map[string]string{"cwd": "/new"}),
	} {
		if err := m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	ctx := m.BuildContext()
	if len(ctx.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(ctx.Messages))
	}
	if messageRole(ctx.Messages[0]) != "system" || messageRole(ctx.Messages[2]) != "system" {
		t.Fatalf("roles = [%s, %s, %s], want system in place", messageRole(ctx.Messages[0]), messageRole(ctx.Messages[1]), messageRole(ctx.Messages[2]))
	}
	// EntryIDs 与消息同序
	if len(ctx.EntryIDs) != 3 {
		t.Fatalf("entryIDs = %d, want 3", len(ctx.EntryIDs))
	}
}

// TestSystemMessageCodecRoundTrip system 消息可编解码往返
func TestSystemMessageCodecRoundTrip(t *testing.T) {
	m := New("/project", t.TempDir())
	original := systemMsg(map[string]string{"preamble": "base", "cwd": "/work"})
	// 先落一条对话消息触发落盘，system 条目随同写入
	if err := m.AppendMessage(userMsg("u1")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(original); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(m.sessionFile, "/project")
	if err != nil {
		t.Fatal(err)
	}
	ctx := reopened.BuildContext()
	if len(ctx.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(ctx.Messages))
	}
	got, ok := ctx.Messages[1].(llm.SystemMessage)
	if !ok {
		t.Fatalf("message = %T, want SystemMessage", ctx.Messages[1])
	}
	if got.Sections["preamble"] != "base" || got.Sections["cwd"] != "/work" {
		t.Errorf("sections = %v, want round-tripped content", got.Sections)
	}
}

// TestCompactionSnapshotLeadsProjection 压缩后投影以快照为首条，保留区内的 system 条目被丢弃
func TestCompactionSnapshotLeadsProjection(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(systemMsg(map[string]string{"preamble": "old"})); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(userMsg("u1")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(assistantMsg("a1")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(userMsg("u2")); err != nil {
		t.Fatal(err)
	}
	kept := m.entries[2].base().ID
	snapshot := &llm.SystemMessage{Sections: map[string]string{"preamble": "snapshot", "cwd": "/work"}}
	if err := m.AppendCompaction("summary text", kept, 900, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	ctx := m.BuildContext()

	// 首条为压缩快照
	if len(ctx.Messages) == 0 {
		t.Fatal("messages empty")
	}
	first, ok := ctx.Messages[0].(llm.SystemMessage)
	if !ok {
		t.Fatalf("first message = %T, want SystemMessage snapshot", ctx.Messages[0])
	}
	if first.Sections["preamble"] != "snapshot" {
		t.Errorf("snapshot preamble = %q, want snapshot content", first.Sections["preamble"])
	}
	// 保留区内的旧 system 条目被丢弃，不再出现旧 preamble
	for i, msg := range ctx.Messages {
		if _, ok := msg.(llm.SystemMessage); ok && i > 0 {
			t.Errorf("messages[%d] is an extra system message, want only the snapshot", i)
		}
	}
}

// TestCompactionWithoutSystemMessage 无 system 消息时快照为空，压缩条目不携带该字段
func TestCompactionWithoutSystemMessage(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(userMsg("u1")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(assistantMsg("a1")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendCompaction("summary", m.entries[1].base().ID, 100, nil, nil); err != nil {
		t.Fatal(err)
	}
	ctx := m.BuildContext()
	for _, msg := range ctx.Messages {
		if _, ok := msg.(llm.SystemMessage); ok {
			t.Fatalf("messages = %+v, want no system message", ctx.Messages)
		}
	}
}

// TestCompactionSnapshotCodecRoundTrip 快照随压缩条目往返
func TestCompactionSnapshotCodecRoundTrip(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(userMsg("u1")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(assistantMsg("a1")); err != nil {
		t.Fatal(err)
	}
	snapshot := &llm.SystemMessage{Sections: map[string]string{"preamble": "snap"}}
	if err := m.AppendCompaction("summary", m.entries[1].base().ID, 100, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(m.sessionFile, "/project")
	if err != nil {
		t.Fatal(err)
	}
	ctx := reopened.BuildContext()
	got, ok := ctx.Messages[0].(llm.SystemMessage)
	if !ok {
		t.Fatalf("first message = %T, want SystemMessage", ctx.Messages[0])
	}
	if got.Sections["preamble"] != "snap" {
		t.Errorf("sections = %v, want round-tripped snapshot", got.Sections)
	}
}
