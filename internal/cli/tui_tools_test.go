package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestToolProjectionDeduplicatesAndTracksResult(t *testing.T) {
	runtime := &tuiRuntime{sequence: 1, currentAssistantID: "assistant-1", toolStarted: map[string]time.Time{}}
	runtime.toolStartLocked(llm.ToolExecutionStartEvent{ToolCallID: "call-1", ToolName: "read", Arguments: map[string]any{"path": "a.go"}})
	runtime.toolStartLocked(llm.ToolExecutionStartEvent{ToolCallID: "call-1", ToolName: "read", Arguments: map[string]any{"path": "a.go"}})
	runtime.toolEndLocked(llm.ToolExecutionEndEvent{ToolCallID: "call-1", ToolName: "read", Result: llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "done"}}}})
	if len(runtime.messages) != 1 || runtime.messages[0].ToolGroup == nil {
		t.Fatalf("messages = %+v", runtime.messages)
	}
	group := runtime.messages[0].ToolGroup
	if len(group.Calls) != 1 || group.Calls[0].Status != tui.ToolSuccess || group.Calls[0].Result != "done" {
		t.Fatalf("group = %+v", group)
	}
}

func TestLiveThinkingUsesContentIndexes(t *testing.T) {
	runtime := &tuiRuntime{sequence: 1, currentAssistantID: "assistant-1"}
	message := llm.AssistantMessage{Content: []llm.Content{
		llm.ThinkingContent{Thinking: "first"},
		llm.TextContent{Text: "answer"},
		llm.ThinkingContent{Thinking: "second"},
	}}
	converted, ok := runtime.liveAssistantLocked(message, "assistant-1", llm.AssistantMessageThinkingDeltaEvent{ContentIndex: 2})
	if !ok || converted.ToolGroup == nil {
		t.Fatalf("converted = %+v", converted)
	}
	if len(converted.ToolGroup.Thinking) != 2 || converted.ToolGroup.Thinking[1].Text != "second" || !converted.ToolGroup.Thinking[1].Active {
		t.Fatalf("thinking blocks = %+v", converted.ToolGroup.Thinking)
	}
}

func TestCompletedThinkingSnapshotIsProjectedWithoutStreamEvent(t *testing.T) {
	runtime := &tuiRuntime{sequence: 1}
	message := llm.AssistantMessage{Content: []llm.Content{llm.ThinkingContent{Thinking: "final reasoning"}}}
	converted, ok := runtime.liveAssistantLocked(message, "assistant-1", nil)
	if !ok || converted.ToolGroup == nil || len(converted.ToolGroup.Thinking) != 1 {
		t.Fatalf("converted = %+v", converted)
	}
	if converted.ToolGroup.Thinking[0].Text != "final reasoning" || converted.ToolGroup.Thinking[0].Active {
		t.Fatalf("thinking = %+v", converted.ToolGroup.Thinking)
	}
}

func TestEmptyAssistantStartShowsWorkingGroup(t *testing.T) {
	runtime := &tuiRuntime{sequence: 1}
	converted, ok := runtime.liveAssistantLocked(llm.AssistantMessage{}, "assistant-1", nil)
	if !ok || converted.ToolGroup == nil || !converted.ToolGroup.Active {
		t.Fatalf("converted = %+v", converted)
	}
	if len(runtime.messages) != 1 || runtime.messages[0].ToolGroup == nil {
		t.Fatalf("messages = %+v", runtime.messages)
	}
}

func TestConsecutiveAssistantThinkingUpdatesReuseToolGroupAnchor(t *testing.T) {
	runtime := &tuiRuntime{sequence: 1}
	first, ok := runtime.liveAssistantLocked(llm.AssistantMessage{Content: []llm.Content{llm.ThinkingContent{Thinking: "first"}}}, "assistant-1", llm.AssistantMessageThinkingDeltaEvent{ContentIndex: 0})
	if !ok || first.ToolGroup == nil {
		t.Fatalf("first = %+v", first)
	}
	second, ok := runtime.liveAssistantLocked(llm.AssistantMessage{Content: []llm.Content{llm.ThinkingContent{Thinking: "second"}}}, "assistant-2", llm.AssistantMessageThinkingDeltaEvent{ContentIndex: 0})
	if !ok || second.ToolGroup == nil || second.ID != "assistant-1" {
		t.Fatalf("second = %+v", second)
	}
	if len(runtime.messages) != 1 || runtime.messages[0].ToolGroup == nil || runtime.messages[0].ToolGroup.Thinking[0].Text != "second" {
		t.Fatalf("messages = %+v", runtime.messages)
	}
}

func TestAgentEndDividerIsBeforeFinalTextProjection(t *testing.T) {
	runtime := &tuiRuntime{turnStarted: time.Now().Add(-2 * time.Second)}
	runtime.messages = []tui.Message{
		{ID: "work", Role: "assistant", ToolGroup: &tui.ToolGroupView{ID: "group", Sealed: true}},
		{ID: "text", Role: "assistant", Text: "finished"},
	}
	runtime.addDividerLocked()
	if runtime.messages[1].Divider == nil || runtime.messages[1].Divider.Elapsed <= 0 {
		t.Fatalf("messages = %+v", runtime.messages)
	}
}

func TestReplaceMessagesPreservesTurnDivider(t *testing.T) {
	runtime := &tuiRuntime{}
	runtime.messages = []tui.Message{{ID: "text", Role: "assistant", Text: "finished", Divider: &tui.TurnDividerView{Elapsed: 3 * time.Second}}}
	runtime.replaceMessagesLocked(session.SessionContext{Messages: []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "finished"}}},
	}})
	if len(runtime.messages) != 1 || runtime.messages[0].Divider == nil || runtime.messages[0].Divider.Elapsed != 3*time.Second {
		t.Fatalf("messages = %+v", runtime.messages)
	}
}

func TestHistoryProjectionMergesConsecutiveToolTurns(t *testing.T) {
	runtime := &tuiRuntime{}
	messages := []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "a", Name: "bash", Arguments: map[string]any{"command": "one"}}}},
		llm.ToolResultMessage{ToolCallID: "a", ToolName: "bash", Content: []llm.Content{llm.TextContent{Text: "one"}}},
		llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "b", Name: "read", Arguments: map[string]any{"path": "b.go"}}}},
		llm.ToolResultMessage{ToolCallID: "b", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "two"}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}},
	}
	projected := runtime.projectSessionMessages(messages)
	if len(projected) != 2 || projected[0].ToolGroup == nil || len(projected[0].ToolGroup.Calls) != 2 || projected[1].Text != "done" {
		t.Fatalf("projected = %+v", projected)
	}
	if strings.Contains(projected[0].ToolGroup.Calls[0].Result, "two") {
		t.Fatal("tool results were merged into the wrong call")
	}
}

func TestHistoryProjectionMarksUnfinishedToolsInterrupted(t *testing.T) {
	runtime := &tuiRuntime{}
	projected := runtime.projectSessionMessages([]llm.Message{
		llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "cancelled", Name: "bash", Arguments: map[string]any{"command": "sleep 1"}}}},
	})
	if len(projected) != 1 || projected[0].ToolGroup == nil || projected[0].ToolGroup.Calls[0].Status != tui.ToolInterrupted {
		t.Fatalf("projected = %+v", projected)
	}
}

func TestHistoryProjectionKeepsCompactionSummaryRole(t *testing.T) {
	runtime := &tuiRuntime{}
	projected := runtime.projectSessionMessages([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "The conversation history before this point was compacted into a summary."}}},
	})
	if len(projected) != 1 || projected[0].Role != "summary" {
		t.Fatalf("projected = %+v", projected)
	}
}
