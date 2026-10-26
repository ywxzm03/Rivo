package ctxview_test

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func textMessage(text string) llm.UserMessage {
	return llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}}
}

// Messages 按条目顺序展开所有消息
func TestMessagesFlattensEntriesInOrder(t *testing.T) {
	view := ctxview.ContextView{Entries: []ctxview.ContextEntry{
		{SourceID: "a", Kind: ctxview.ContextMessage, Messages: []llm.Message{textMessage("one"), textMessage("two")}},
		{SourceID: "b", Kind: ctxview.ContextCompaction, Messages: []llm.Message{textMessage("three")}, Summary: "s"},
	}}
	messages := view.Messages()
	if len(messages) != 3 {
		t.Fatalf("Messages() = %d, want 3", len(messages))
	}
	if got := messages[2].(llm.UserMessage).Content[0].(llm.TextContent).Text; got != "three" {
		t.Errorf("third message = %q, want three", got)
	}
}

// MessageCount 统计全部条目里的消息总数
func TestMessageCountSumsEntries(t *testing.T) {
	entries := []ctxview.ContextEntry{
		{Messages: []llm.Message{textMessage("a")}},
		{Messages: []llm.Message{textMessage("b"), textMessage("c")}},
		{Messages: nil},
	}
	if got := ctxview.MessageCount(entries); got != 3 {
		t.Errorf("MessageCount() = %d, want 3", got)
	}
}

// SummaryMessages 排除 compaction 条目与 system 消息
func TestSummaryMessagesSkipsCompactionAndSystem(t *testing.T) {
	system := llm.SystemMessage{Sections: map[string]string{"preamble": "sys"}}
	entries := []ctxview.ContextEntry{
		{Kind: ctxview.ContextMessage, Messages: []llm.Message{textMessage("keep"), system}},
		{Kind: ctxview.ContextCompaction, Messages: []llm.Message{textMessage("drop")}},
	}
	messages := ctxview.SummaryMessages(entries)
	if len(messages) != 1 {
		t.Fatalf("SummaryMessages() = %d, want 1", len(messages))
	}
	if got := messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text; got != "keep" {
		t.Errorf("kept message = %q, want keep", got)
	}
}

// WrappedSummary 用固定前后缀包裹摘要，回放格式必须稳定
func TestWrappedSummaryUsesStableWrapper(t *testing.T) {
	got := ctxview.WrappedSummary("body")
	want := "The conversation history before this point was compacted into the following summary:\n\n<summary>\nbody\n</summary>"
	if got != want {
		t.Errorf("WrappedSummary() = %q, want %q", got, want)
	}
}
