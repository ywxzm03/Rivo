package llm

import "testing"

func TestContentTypesAndInterfaceMethods(t *testing.T) {
	tests := []struct {
		name    string
		content Content
		want    ContentType
	}{
		{name: "text", content: TextContent{Text: "text"}, want: ContentText},
		{name: "image", content: ImageContent{Data: "data", MIMEType: "image/png"}, want: ContentImage},
		{name: "thinking", content: ThinkingContent{Thinking: "plan", ThinkingSignature: "signature"}, want: ContentThinking},
		{name: "tool call", content: ToolCall{ID: "call", Name: "read", Arguments: map[string]any{}}, want: ContentToolCall},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.content.Type(); got != tt.want {
				t.Fatalf("content type = %q, want %q", got, tt.want)
			}
			tt.content.content()
		})
	}
}

func TestMessageRolesAndInterfaceMethods(t *testing.T) {
	tests := []struct {
		name    string
		message Message
		want    Role
	}{
		{name: "user", message: UserMessage{}, want: RoleUser},
		{name: "assistant", message: AssistantMessage{}, want: RoleAssistant},
		{name: "tool result", message: ToolResultMessage{}, want: RoleToolResult},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.message.Role(); got != tt.want {
				t.Fatalf("message role = %q, want %q", got, tt.want)
			}
			tt.message.message()
		})
	}
}

func TestAssistantMessageEventTypesAndInterfaceMethods(t *testing.T) {
	tests := []struct {
		name  string
		event AssistantMessageEvent
		want  string
	}{
		{name: "start", event: AssistantMessageStartEvent{}, want: EventStart},
		{name: "text start", event: AssistantMessageTextStartEvent{}, want: EventTextStart},
		{name: "text delta", event: AssistantMessageTextDeltaEvent{}, want: EventTextDelta},
		{name: "text end", event: AssistantMessageTextEndEvent{}, want: EventTextEnd},
		{name: "thinking start", event: AssistantMessageThinkingStartEvent{}, want: EventThinkingStart},
		{name: "thinking delta", event: AssistantMessageThinkingDeltaEvent{}, want: EventThinkingDelta},
		{name: "thinking end", event: AssistantMessageThinkingEndEvent{}, want: EventThinkingEnd},
		{name: "tool call start", event: AssistantMessageToolCallStartEvent{}, want: EventToolCallStart},
		{name: "tool call delta", event: AssistantMessageToolCallDeltaEvent{}, want: EventToolCallDelta},
		{name: "tool call end", event: AssistantMessageToolCallEndEvent{}, want: EventToolCallEnd},
		{name: "done", event: AssistantMessageDoneEvent{}, want: EventDone},
		{name: "error", event: AssistantMessageErrorEvent{}, want: EventError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Type(); got != tt.want {
				t.Fatalf("event type = %q, want %q", got, tt.want)
			}
			tt.event.assistantMessageEvent()
		})
	}
}

func TestAgentEventTypesAndInterfaceMethods(t *testing.T) {
	tests := []struct {
		name  string
		event AgentEvent
		want  string
	}{
		{name: "agent start", event: AgentStartEvent{}, want: AgentEventAgentStart},
		{name: "agent end", event: AgentEndEvent{}, want: AgentEventAgentEnd},
		{name: "turn start", event: TurnStartEvent{}, want: AgentEventTurnStart},
		{name: "turn end", event: TurnEndEvent{}, want: AgentEventTurnEnd},
		{name: "message start", event: MessageStartEvent{}, want: AgentEventMessageStart},
		{name: "message update", event: MessageUpdateEvent{}, want: AgentEventMessageUpdate},
		{name: "message end", event: MessageEndEvent{}, want: AgentEventMessageEnd},
		{name: "tool execution start", event: ToolExecutionStartEvent{}, want: AgentEventToolExecutionStart},
		{name: "tool execution update", event: ToolExecutionUpdateEvent{}, want: AgentEventToolExecutionUpdate},
		{name: "tool execution end", event: ToolExecutionEndEvent{}, want: AgentEventToolExecutionEnd},
		{name: "warning", event: WarningEvent{}, want: AgentEventWarning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Type(); got != tt.want {
				t.Fatalf("event type = %q, want %q", got, tt.want)
			}
			tt.event.agentEvent()
		})
	}
}
