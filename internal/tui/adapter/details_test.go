package adapter

import (
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// edit 的结构化详情单独投影，结果正文里不再混入 JSON
func TestToolDetailsProjectedSeparately(t *testing.T) {
	runtime := &Runtime{sequence: 1, currentAssistantID: "assistant-1", toolStarted: map[string]time.Time{}}
	runtime.toolStartLocked(llm.ToolExecutionStartEvent{ToolCallID: "call-1", ToolName: "edit", Arguments: map[string]any{"path": "a.go"}})
	runtime.toolEndLocked(llm.ToolExecutionEndEvent{ToolCallID: "call-1", ToolName: "edit", Result: llm.ToolResult{
		Content: []llm.Content{llm.TextContent{Text: "Successfully replaced 1 block(s) in a.go."}},
		Details: map[string]any{"Patch": "@@ -1 +1 @@\n-a\n+b", "Diff": "-1 a\n+1 b"},
	}})
	call := runtime.messages[0].ToolGroup.Calls[0]
	if call.Result != "Successfully replaced 1 block(s) in a.go." {
		t.Fatalf("result = %q, want text only", call.Result)
	}
	if !strings.Contains(call.Details, `"Patch"`) {
		t.Fatalf("details = %q, want patch JSON", call.Details)
	}
}

// 回放会话时同样把详情放进 ToolCallView
func TestToolDetailsProjectedFromSessionReplay(t *testing.T) {
	runtime := &Runtime{sequence: 1}
	view := ctxview.ContextView{Entries: []ctxview.ContextEntry{{
		SourceID: "entry-1",
		Messages: []llm.Message{
			llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "call-1", Name: "edit", Arguments: map[string]any{"path": "a.go"}}}},
			llm.ToolResultMessage{ToolCallID: "call-1", ToolName: "edit", Content: []llm.Content{llm.TextContent{Text: "ok"}}, Details: map[string]any{"Patch": "@@ -1 +1 @@\n-a\n+b"}},
		},
	}}}
	messages := runtime.projectContext(view)
	if len(messages) != 1 || messages[0].ToolGroup == nil || len(messages[0].ToolGroup.Calls) != 1 {
		t.Fatalf("projected = %+v", messages)
	}
	if details := messages[0].ToolGroup.Calls[0].Details; !strings.Contains(details, "Patch") {
		t.Fatalf("replayed details = %q", details)
	}
}

// 流式快照到达时，已存在的调用要跟着更新名字和参数：
// 否则从宣告 tool_use 到工具真正执行之间会一直显示空标签
func TestToolCallFollowsStreamingSnapshot(t *testing.T) {
	runtime := &Runtime{sequence: 1, currentAssistantID: "assistant-1", assistantTurn: 1}
	runtime.liveAssistantLocked(llm.AssistantMessage{Content: []llm.Content{
		llm.ToolCall{ID: "c1", Arguments: map[string]any{}},
	}}, "assistant-1", llm.AssistantMessageToolCallStartEvent{ContentIndex: 0, ToolCall: llm.ToolCall{ID: "c1"}})
	call := runtime.messages[0].ToolGroup.Calls[0]
	if call.Name != "" || call.ArgsComplete {
		t.Fatalf("start call = %+v, want empty name and incomplete args", call)
	}

	runtime.liveAssistantLocked(llm.AssistantMessage{Content: []llm.Content{
		llm.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "/tmp/a"}},
	}}, "assistant-1", llm.AssistantMessageToolCallDeltaEvent{ContentIndex: 0, Delta: `{"path":"/tmp/a"}`})
	call = runtime.messages[0].ToolGroup.Calls[0]
	if call.Name != "read" || !strings.Contains(call.Arguments, "/tmp/a") {
		t.Fatalf("delta call = %+v, want the streamed name and arguments", call)
	}
	if call.ArgsComplete {
		t.Fatalf("args marked complete before the toolcall ended: %+v", call)
	}

	runtime.liveAssistantLocked(llm.AssistantMessage{Content: []llm.Content{
		llm.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "/tmp/a"}},
	}}, "assistant-1", llm.AssistantMessageToolCallEndEvent{ContentIndex: 0, ToolCall: llm.ToolCall{ID: "c1", Name: "read"}})
	if call = runtime.messages[0].ToolGroup.Calls[0]; !call.ArgsComplete {
		t.Fatalf("args not marked complete after toolcall end: %+v", call)
	}
}

// 每一轮 assistant 的思考各自保留，不覆盖前一轮
func TestThinkingKeptPerRound(t *testing.T) {
	runtime := &Runtime{sequence: 1, currentAssistantID: "assistant-1", assistantTurn: 1, toolStarted: map[string]time.Time{}}
	runtime.liveAssistantLocked(llm.AssistantMessage{Content: []llm.Content{
		llm.ThinkingContent{Thinking: "first round"},
		llm.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a"}},
	}}, "assistant-1", llm.AssistantMessageThinkingDeltaEvent{ContentIndex: 0})
	runtime.toolStartLocked(llm.ToolExecutionStartEvent{ToolCallID: "c1", ToolName: "read", Arguments: map[string]any{"path": "a"}})

	runtime.assistantTurn = 2
	runtime.currentAssistantID = "assistant-2"
	runtime.liveAssistantLocked(llm.AssistantMessage{Content: []llm.Content{
		llm.ThinkingContent{Thinking: "second round"},
		llm.ToolCall{ID: "c2", Name: "grep", Arguments: map[string]any{"pattern": "p"}},
	}}, "assistant-2", llm.AssistantMessageThinkingDeltaEvent{ContentIndex: 0})

	group := runtime.messages[0].ToolGroup
	if len(group.Thinking) != 2 {
		t.Fatalf("thinking blocks = %+v, want one per round", group.Thinking)
	}
	if group.Thinking[0].Text != "first round" || group.Thinking[0].Round != 1 {
		t.Fatalf("first block = %+v", group.Thinking[0])
	}
	if group.Thinking[1].Text != "second round" || group.Thinking[1].Round != 2 {
		t.Fatalf("second block = %+v", group.Thinking[1])
	}
	for _, call := range group.Calls {
		if call.Round == 0 {
			t.Fatalf("call lost its round: %+v", call)
		}
	}
}

// 执行事件补建的调用属于当前轮，否则会被排到所有 thinking 之前
func TestExecutionCreatedCallKeepsRound(t *testing.T) {
	runtime := &Runtime{sequence: 1, currentAssistantID: "assistant-1", assistantTurn: 3, toolStarted: map[string]time.Time{}}
	runtime.toolStartLocked(llm.ToolExecutionStartEvent{ToolCallID: "c9", ToolName: "bash", Arguments: map[string]any{"command": "x"}})
	call := runtime.messages[0].ToolGroup.Calls[0]
	if call.Round != 3 || !call.ArgsComplete {
		t.Fatalf("call = %+v, want round 3 with complete args", call)
	}
}
