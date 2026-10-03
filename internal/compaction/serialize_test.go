package compaction

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestSerializeMessages(t *testing.T) {
	messages := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "fix the bug"}}},
		llm.AssistantMessage{Content: []llm.Content{
			llm.ThinkingContent{Thinking: "plan it"},
			llm.TextContent{Text: "on it"},
			llm.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.txt"}},
		}},
		llm.ToolResultMessage{ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "file body"}}},
	}
	got := SerializeMessages(messages)
	for _, want := range []string{
		"[User]: fix the bug",
		"[Assistant thinking]: plan it",
		"[Assistant]: on it",
		`[Assistant tool calls]: read({"path":"a.txt"})`,
		"[Tool result]: read: file body",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("serialized = %q, want contains %q", got, want)
		}
	}
}

func TestSerializeMessagesTruncatesToolResult(t *testing.T) {
	long := strings.Repeat("x", ToolResultCharLimit+100)
	got := SerializeMessages([]llm.Message{
		llm.ToolResultMessage{ToolName: "read", Content: []llm.Content{llm.TextContent{Text: long}}},
	})
	if !strings.Contains(got, "[... 100 more characters truncated]") {
		t.Errorf("serialized = %q, want truncation marker", got)
	}
	if strings.Contains(got, strings.Repeat("x", ToolResultCharLimit+50)) {
		t.Error("tool result was not truncated")
	}
	// 未超长时不加标记
	full := strings.Repeat("y", ToolResultCharLimit)
	got = SerializeMessages([]llm.Message{
		llm.ToolResultMessage{ToolName: "read", Content: []llm.Content{llm.TextContent{Text: full}}},
	})
	if strings.Contains(got, "truncated") {
		t.Errorf("short tool result should not be truncated: %q", got)
	}
}

func TestSerializeMessagesImagePlaceholder(t *testing.T) {
	got := SerializeMessages([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.ImageContent{MIMEType: "image/png"}}},
	})
	if !strings.Contains(got, "[image: image/png]") {
		t.Errorf("serialized = %q, want image placeholder", got)
	}
}
