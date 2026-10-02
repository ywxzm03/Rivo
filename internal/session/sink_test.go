package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestSinkPersistsAndForwards(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	var forwarded []string
	sink := Sink(m, func(event llm.AgentEvent) error {
		forwarded = append(forwarded, event.Type())
		return nil
	})

	// 1.非 message_end 事件只转发不落盘
	if err := sink(llm.MessageStartEvent{Message: userMsg("hi")}); err != nil {
		t.Fatalf("sink(MessageStartEvent) error = %v", err)
	}
	// 2.message_end 追加进会话后转发
	if err := sink(llm.MessageEndEvent{Message: userMsg("hi")}); err != nil {
		t.Fatalf("sink(user message_end) error = %v", err)
	}
	if err := sink(llm.MessageEndEvent{Message: assistantMsg("hello")}); err != nil {
		t.Fatalf("sink(assistant message_end) error = %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("jsonl files = %v, want exactly 1", matches)
	}
	reloaded, err := Open(matches[0], "/project")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	ctx := reloaded.BuildContext()
	if len(ctx.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(ctx.Messages))
	}
	want := []string{llm.AgentEventMessageStart, llm.AgentEventMessageEnd, llm.AgentEventMessageEnd}
	if len(forwarded) != len(want) {
		t.Fatalf("forwarded = %v, want %v", forwarded, want)
	}
	for i := range want {
		if forwarded[i] != want[i] {
			t.Errorf("forwarded[%d] = %q, want %q", i, forwarded[i], want[i])
		}
	}
}

func TestSinkPropagatesPersistError(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	// 模拟残留文件使独占创建失败
	if err := os.WriteFile(m.sessionFile, []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := Sink(m, func(event llm.AgentEvent) error {
		t.Fatal("next sink should not be called after persist failure")
		return nil
	})
	if err := sink(llm.MessageEndEvent{Message: userMsg("hi")}); err == nil {
		t.Fatal("sink() error = nil, want persist failure propagated")
	}
}
