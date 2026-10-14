package adapter

import (
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// 空 partial 的 aborted 消息必须无条件进入投影（对齐 Pi：abort 必有可见反馈）
func TestAbortedEmptyPartialEntersProjection(t *testing.T) {
	runtime := &Runtime{
		sequence:           1,
		currentAssistantID: "assistant-1",
		toolStarted:        map[string]time.Time{},
	}
	// 模拟 handleEvent 收到 MessageEnd(aborted, 空 content)
	runtime.handleEventForTest(llm.MessageEndEvent{Message: llm.AssistantMessage{
		Content:      []llm.Content{llm.TextContent{Text: ""}},
		StopReason:   llm.StopReasonAborted,
		ErrorMessage: "Operation aborted",
	}})
	// dispatch 收尾
	var partial *tui.Message
	partial = runtime.partialAssistantLocked()
	runtime.replaceMessagesLocked(testContext([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}},
	}))
	if partial != nil && !runtime.hasMessageLocked(*partial) {
		runtime.upsertLocked(*partial)
	}
	found := false
	for _, message := range runtime.messages {
		if message.Aborted {
			found = true
		}
	}
	if !found {
		t.Fatalf("empty aborted message lost from projection: %+v", runtime.messages)
	}
}

// 带 ToolGroup 的 aborted 消息：工具组保留且带 Aborted 标记
func TestAbortedWithToolGroupKeepsGroup(t *testing.T) {
	runtime := &Runtime{
		sequence:           1,
		currentAssistantID: "assistant-1",
		toolStarted:        map[string]time.Time{},
	}
	runtime.handleEventForTest(llm.ToolExecutionStartEvent{ToolCallID: "call-1", ToolName: "bash", Arguments: map[string]any{"command": "sleep 30"}})
	runtime.handleEventForTest(llm.MessageEndEvent{Message: llm.AssistantMessage{
		Content:      []llm.Content{llm.TextContent{Text: ""}},
		StopReason:   llm.StopReasonAborted,
		ErrorMessage: "Operation aborted",
	}})
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	for _, message := range runtime.messages {
		if message.Aborted {
			if message.ToolGroup == nil || len(message.ToolGroup.Calls) != 1 {
				t.Fatalf("tool group lost on aborted message: %+v", message)
			}
			return
		}
	}
	t.Fatalf("no aborted message: %+v", runtime.messages)
}

// partialAssistantLocked 必须能取回空内容的 aborted 消息
func TestPartialAssistantReturnsAbortedEmpty(t *testing.T) {
	runtime := &Runtime{
		sequence:           1,
		currentAssistantID: "assistant-1",
		toolStarted:        map[string]time.Time{},
	}
	runtime.upsertLocked(tui.Message{ID: "assistant-1", Role: "assistant", Aborted: true})
	if partial := runtime.partialAssistantLocked(); partial == nil || !partial.Aborted {
		t.Fatalf("partial = %+v, want aborted message", partial)
	}
}

// handleEventForTest 直接调用私有 handleEvent（同包测试辅助）
func (r *Runtime) handleEventForTest(event llm.AgentEvent) {
	r.handleEvent(event)
}
