package llm

import "testing"

func TestAgentEventTypesAndPayloads(t *testing.T) {
	types := []struct {
		event    AgentEvent
		want     string
		wantName string
	}{
		{AgentStartEvent{}, AgentEventAgentStart, "agent_start"},
		{AgentEndEvent{}, AgentEventAgentEnd, "agent_end"},
		{TurnStartEvent{}, AgentEventTurnStart, "turn_start"},
		{TurnEndEvent{}, AgentEventTurnEnd, "turn_end"},
		{MessageStartEvent{}, AgentEventMessageStart, "message_start"},
		{MessageUpdateEvent{}, AgentEventMessageUpdate, "message_update"},
		{MessageEndEvent{}, AgentEventMessageEnd, "message_end"},
		{ToolExecutionStartEvent{}, AgentEventToolExecutionStart, "tool_execution_start"},
		{ToolExecutionUpdateEvent{}, AgentEventToolExecutionUpdate, "tool_execution_update"},
		{ToolExecutionEndEvent{}, AgentEventToolExecutionEnd, "tool_execution_end"},
	}
	for _, test := range types {
		if got := test.event.Type(); got != test.want || got != test.wantName {
			t.Errorf("event type = %q, want %q (%q)", got, test.want, test.wantName)
		}
	}

	assistant := AssistantMessage{Content: []Content{TextContent{Text: "partial"}}}
	agentEnd := AgentEndEvent{Messages: []Message{assistant}}
	if len(agentEnd.Messages) != 1 {
		t.Fatalf("agent_end messages = %#v", agentEnd.Messages)
	}

	toolResult := ToolResultMessage{ToolCallID: "call-1", ToolName: "read"}
	modelEvent := AssistantMessageTextDeltaEvent{Delta: "partial"}
	args := map[string]any{"path": "file.txt"}
	result := ToolResult{Content: []Content{TextContent{Text: "output"}}}

	update := MessageUpdateEvent{
		Message:               assistant,
		AssistantMessageEvent: modelEvent,
	}
	partial, ok := update.Message.(AssistantMessage)
	if !ok || partial.Content[0].(TextContent).Text != "partial" {
		t.Fatalf("message_update partial message = %#v", update.Message)
	}
	textEvent, ok := update.AssistantMessageEvent.(AssistantMessageTextDeltaEvent)
	if !ok || textEvent.Type() != EventTextDelta || textEvent.Delta != "partial" {
		t.Fatalf("message_update source event = %+v", update.AssistantMessageEvent)
	}

	end := TurnEndEvent{
		Message:     assistant,
		ToolResults: []ToolResultMessage{toolResult},
	}
	if _, ok := end.Message.(AssistantMessage); !ok || len(end.ToolResults) != 1 || end.ToolResults[0].ToolCallID != "call-1" {
		t.Fatalf("turn_end payload = %+v", end)
	}

	start := ToolExecutionStartEvent{ToolCallID: "call-1", ToolName: "read", Arguments: args}
	if start.Arguments["path"] != "file.txt" {
		t.Fatalf("tool_execution_start arguments = %#v", start.Arguments)
	}
	toolUpdate := ToolExecutionUpdateEvent{PartialResult: result}
	toolEnd := ToolExecutionEndEvent{Result: result, IsError: true}
	if len(toolUpdate.PartialResult.Content) != 1 || len(toolEnd.Result.Content) != 1 || !toolEnd.IsError {
		t.Fatalf("tool execution payloads = update:%+v end:%+v", toolUpdate, toolEnd)
	}
}
