package session

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSinkPersistsCompaction(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	sink := Sink(m, nil)
	// 1.四条消息按序落盘，镜像与消息序对齐
	for _, msg := range []llm.Message{
		userMsg("hi"),
		assistantMsg("plan"),
		userMsg("more"),
		assistantMsg("done"),
	} {
		if err := sink(llm.MessageEndEvent{Message: msg}); err != nil {
			t.Fatalf("sink(message_end) error = %v", err)
		}
	}
	// 2.压缩事件：保留尾部 2 条消息，压缩点落盘且 FirstKeptEntryID 指向第三条消息
	err := sink(llm.CompactionEndEvent{
		Reason:           "auto",
		Summary:          "session summary",
		KeptMessageCount: 2,
		TokensBefore:     9000,
		Usage:            llm.Usage{InputTokens: 10, OutputTokens: 5},
	})
	if err != nil {
		t.Fatalf("sink(CompactionEndEvent) error = %v", err)
	}
	reloaded := reloadSession(t, dir)
	ctx := reloaded.BuildContext()
	// 3.回放结果应为 [摘要 user 消息, 第三条, 第四条]
	if len(ctx.Messages) != 3 {
		t.Fatalf("replayed messages = %d, want 3", len(ctx.Messages))
	}
	if !strings.Contains(replayText(t, ctx.Messages[0]), "session summary") {
		t.Errorf("messages[0] = %#v, want wrapped summary", ctx.Messages[0])
	}
	if replayText(t, ctx.Messages[1]) != "more" {
		t.Errorf("messages[1] = %q, want kept message", replayText(t, ctx.Messages[1]))
	}
	if ctx.LastSummary != "session summary" {
		t.Errorf("LastSummary = %q, want session summary", ctx.LastSummary)
	}
	// 4.第二次压缩：镜像已重置，保留 1 条时 FirstKeptEntryID 应跳过摘要占位
	if err := sink(llm.MessageEndEvent{Message: userMsg("after")}); err != nil {
		t.Fatalf("sink(message_end) after compaction error = %v", err)
	}
	if err := sink(llm.CompactionEndEvent{Reason: "auto", Summary: "second", KeptMessageCount: 2, TokensBefore: 100}); err != nil {
		t.Fatalf("second compaction error = %v", err)
	}
	reloaded = reloadSession(t, dir)
	ctx = reloaded.BuildContext()
	if len(ctx.Messages) != 3 {
		t.Fatalf("second replay messages = %d, want 3 (summary, done, after)", len(ctx.Messages))
	}
	if ctx.LastSummary != "second" {
		t.Errorf("second LastSummary = %q, want second", ctx.LastSummary)
	}
}

func TestSinkCompactionWithoutUsageOmitsUsage(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	sink := Sink(m, nil)
	if err := sink(llm.MessageEndEvent{Message: userMsg("hi")}); err != nil {
		t.Fatal(err)
	}
	if err := sink(llm.CompactionEndEvent{Reason: "manual", Summary: "s", KeptMessageCount: 0}); err != nil {
		t.Fatal(err)
	}
	// 零保留：FirstKeptEntryID 存压缩点自身 ID，回放只剩摘要
	reloaded := reloadSession(t, dir)
	ctx := reloaded.BuildContext()
	if len(ctx.Messages) != 1 {
		t.Fatalf("replayed messages = %d, want 1", len(ctx.Messages))
	}
	for _, e := range reloaded.entries {
		if c, ok := e.(*CompactionEntry); ok {
			if c.Usage != nil {
				t.Errorf("usage = %+v, want omitted", c.Usage)
			}
			if c.FirstKeptEntryID != c.ID {
				t.Errorf("FirstKeptEntryID = %q, want self %q for retain-none", c.FirstKeptEntryID, c.ID)
			}
		}
	}
}

func reloadSession(t *testing.T, dir string) *SessionManager {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("jsonl files = %v, want exactly 1", matches)
	}
	m, err := Open(matches[0], "/project")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return m
}

func replayText(t *testing.T, msg llm.Message) string {
	t.Helper()
	switch m := msg.(type) {
	case llm.UserMessage:
		if text, ok := m.Content[0].(llm.TextContent); ok {
			return text.Text
		}
	case llm.AssistantMessage:
		if text, ok := m.Content[0].(llm.TextContent); ok {
			return text.Text
		}
	}
	t.Fatalf("unexpected message %#v", msg)
	return ""
}
