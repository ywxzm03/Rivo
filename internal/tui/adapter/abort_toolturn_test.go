package adapter

import (
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 复刻截图场景：turn1 toolUse（工具组）→ 工具批次中取消 → turn2 aborted（空）
// 最终投影必须保留带 Aborted 标记的消息
func TestAbortDuringToolTurnKeepsAbortedMarker(t *testing.T) {
	runtime := &Runtime{
		sequence:           1,
		currentAssistantID: "assistant-1",
		toolStarted:        map[string]time.Time{},
	}
	sink := func(event llm.AgentEvent) { runtime.handleEventForTest(event) }

	// turn1: assistant 带 toolUse
	sink(llm.MessageStartEvent{Message: llm.AssistantMessage{}})
	sink(llm.MessageEndEvent{Message: llm.AssistantMessage{
		Content: []llm.Content{
			llm.ToolCall{ID: "c1", Name: "ls", Arguments: map[string]any{}},
			llm.ToolCall{ID: "c2", Name: "bash", Arguments: map[string]any{}},
			llm.ToolCall{ID: "c3", Name: "read", Arguments: map[string]any{}},
		},
		StopReason: llm.StopReasonToolUse,
	}})
	// 工具批次
	sink(llm.ToolExecutionStartEvent{ToolCallID: "c1", ToolName: "ls", Arguments: map[string]any{}})
	sink(llm.ToolExecutionEndEvent{ToolCallID: "c1", ToolName: "ls", Result: llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "files"}}, IsError: false}})
	sink(llm.ToolExecutionStartEvent{ToolCallID: "c2", ToolName: "bash", Arguments: map[string]any{}})
	// —— Escape：工具批次 aborted 收场 ——
	sink(llm.ToolExecutionEndEvent{ToolCallID: "c2", ToolName: "bash", Result: llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "Operation aborted"}}, IsError: true}})
	// turn2: 空的 aborted assistant（下一轮流立即收场）
	sink(llm.MessageStartEvent{Message: llm.AssistantMessage{}})
	sink(llm.MessageEndEvent{Message: llm.AssistantMessage{
		Content:      []llm.Content{llm.TextContent{Text: ""}},
		StopReason:   llm.StopReasonAborted,
		ErrorMessage: "Operation aborted",
	}})
	sink(llm.AgentEndEvent{Messages: nil})

	// dispatch 收尾：会话投影包含全部已落盘消息（含 aborted assistant，E2E 已证实落盘）
	partial := runtime.partialAssistantLocked()
	runtime.replaceMessagesLocked(testContext([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "分析一下当前项目"}}},
		llm.AssistantMessage{
			Content: []llm.Content{
				llm.ToolCall{ID: "c1", Name: "ls", Arguments: map[string]any{}},
			},
			StopReason: llm.StopReasonToolUse,
		},
		llm.AssistantMessage{
			Content:      []llm.Content{llm.TextContent{Text: ""}},
			StopReason:   llm.StopReasonAborted,
			ErrorMessage: "Operation aborted",
		},
	}))
	if partial != nil && !runtime.hasMessageLocked(*partial) {
		runtime.upsertLocked(*partial)
	}

	found := false
	for i, message := range runtime.messages {
		calls := 0
		if message.ToolGroup != nil {
			calls = len(message.ToolGroup.Calls)
		}
		t.Logf("msg[%d] id=%s role=%s aborted=%v group=%v calls=%d", i, message.ID, message.Role, message.Aborted, message.ToolGroup != nil, calls)
		if message.Aborted {
			found = true
		}
	}
	if !found {
		t.Fatal("aborted marker lost after tool-turn cancel")
	}
}
