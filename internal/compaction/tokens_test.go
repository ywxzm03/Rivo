package compaction

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{text: "", want: 0},
		{text: "abcd", want: 1},
		{text: "abcde", want: 2},
		{text: "中文中文", want: 1},
	}
	for _, tt := range tests {
		if got := EstimateTokens(tt.text); got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

func TestMessageTokens(t *testing.T) {
	user := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "abcdefgh"}}}
	if got := MessageTokens(user); got != 2 {
		t.Errorf("user tokens = %d, want 2", got)
	}
	// thinking 不计入上下文预算
	assistant := llm.AssistantMessage{Content: []llm.Content{
		llm.ThinkingContent{Thinking: "abcdefgh"},
		llm.TextContent{Text: "abcdefgh"},
	}}
	if got := MessageTokens(assistant); got != 2 {
		t.Errorf("assistant tokens = %d, want 2 (thinking excluded)", got)
	}
	image := llm.UserMessage{Content: []llm.Content{llm.ImageContent{MIMEType: "image/png"}}}
	if got := MessageTokens(image); got != imageTokenEstimate {
		t.Errorf("image tokens = %d, want %d", got, imageTokenEstimate)
	}
	toolResult := llm.ToolResultMessage{ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "abcdefgh"}}}
	if got := MessageTokens(toolResult); got != 2 {
		t.Errorf("tool result tokens = %d, want 2", got)
	}
}

func TestContextTokensPrefersLastAssistantUsage(t *testing.T) {
	messages := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: strings.Repeat("a", 100)}}},
		llm.AssistantMessage{Usage: llm.Usage{InputTokens: 5000, OutputTokens: 10}},
		llm.ToolResultMessage{ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "abcdefgh"}}},
	}
	// 基准取最后一条 assistant 的真实 usage，其后的工具结果按 chars/4 估算
	if got := ContextTokens(messages); got != 5002 {
		t.Errorf("ContextTokens = %d, want 5002", got)
	}
}

func TestContextTokensFallsBackToEstimate(t *testing.T) {
	messages := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "abcdefgh"}}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "abcdefgh"}}},
	}
	if got := ContextTokens(messages); got != 4 {
		t.Errorf("ContextTokens = %d, want 4", got)
	}
	if got := ContextTokens(nil); got != 0 {
		t.Errorf("ContextTokens(nil) = %d, want 0", got)
	}
}

func TestShouldCompact(t *testing.T) {
	if ShouldCompact(100, 0, 2000) {
		t.Error("unknown context window should never trigger")
	}
	if ShouldCompact(7999, 10000, 2000) {
		t.Error("7999 below threshold should not trigger")
	}
	if !ShouldCompact(8001, 10000, 2000) {
		t.Error("8001 above threshold should trigger")
	}
}
