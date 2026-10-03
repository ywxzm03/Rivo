package compaction

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// turnFixture 造两个完整轮次：每条消息约 100 token
func turnFixture() []llm.Message {
	text := textOf(400)
	call := llm.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": text}}
	return []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}},
		llm.AssistantMessage{Content: []llm.Content{call}},
		llm.ToolResultMessage{ToolName: "read", Content: []llm.Content{llm.TextContent{Text: text}}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}},
		llm.AssistantMessage{Content: []llm.Content{call}},
		llm.ToolResultMessage{ToolName: "read", Content: []llm.Content{llm.TextContent{Text: text}}},
	}
}

func textOf(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestFindCutPointAtTurnBoundary(t *testing.T) {
	messages := turnFixture()
	// 预算 250 token：向前累计到第二条 user 消息时超过预算，切点应落在它之前
	plan := FindCutPoint(messages, 0, 250)
	if plan.CutIndex != 3 {
		t.Fatalf("CutIndex = %d, want 3", plan.CutIndex)
	}
	if plan.IsSplitTurn {
		t.Fatalf("turn-boundary cut should not be split: %+v", plan)
	}
	if plan.TurnStartIndex != -1 {
		t.Fatalf("TurnStartIndex = %d, want -1", plan.TurnStartIndex)
	}
}

func TestFindCutPointNeverLandsOnToolResult(t *testing.T) {
	messages := turnFixture()
	// 预算 50：从尾部累计第一条就是工具结果，切点必须滑过它，最终零保留
	plan := FindCutPoint(messages, 0, 50)
	if plan.CutIndex != len(messages) {
		t.Fatalf("CutIndex = %d, want %d (retain none)", plan.CutIndex, len(messages))
	}
}

func TestFindCutPointSplitTurn(t *testing.T) {
	messages := turnFixture()
	// 预算 150：累计到第二条 assistant 时超过预算，保留尾部以 assistant 开头 → split turn
	plan := FindCutPoint(messages, 0, 150)
	if !plan.IsSplitTurn {
		t.Fatalf("expected split turn, got %+v", plan)
	}
	if plan.TurnStartIndex != 3 {
		t.Fatalf("TurnStartIndex = %d, want 3", plan.TurnStartIndex)
	}
	if plan.CutIndex != 4 {
		t.Fatalf("CutIndex = %d, want 4", plan.CutIndex)
	}
}

func TestFindCutPointRespectsStartOffset(t *testing.T) {
	messages := turnFixture()
	// start=1 模拟跳过回插的摘要消息：切点不得越回 start 之前
	plan := FindCutPoint(messages, 1, 250)
	if plan.CutIndex != 3 {
		t.Fatalf("CutIndex = %d, want 3", plan.CutIndex)
	}
	if plan.CutIndex <= 1 {
		t.Fatalf("CutIndex = %d, want > 1", plan.CutIndex)
	}
}

func TestFindCutPointSmallHistoryDegradesToKeepLast(t *testing.T) {
	messages := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: textOf(400)}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: textOf(400)}}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: textOf(400)}}},
	}
	// 历史总量小于保留预算却仍要求压缩：只保留最新一条，保证有历史可摘要
	plan := FindCutPoint(messages, 0, 20000)
	if plan.CutIndex != 2 {
		t.Fatalf("CutIndex = %d, want 2", plan.CutIndex)
	}
}
