package llm

import "context"

type EventType string

const (
	EventTextDelta EventType = "text_delta"
	EventToolCall  EventType = "toolcall"
	EventDone      EventType = "done"
	EventError     EventType = "error"
)

type Event struct {
	Type     EventType
	Delta    string
	ToolCall ToolCall
	Message  *AssistantMessage
	Err      error
}

const (
	AgentEventAgentStart          = "agent_start"
	AgentEventAgentEnd            = "agent_end"
	AgentEventTurnStart           = "turn_start"
	AgentEventTurnEnd             = "turn_end"
	AgentEventMessageStart        = "message_start"
	AgentEventMessageUpdate       = "message_update"
	AgentEventMessageEnd          = "message_end"
	AgentEventToolExecutionStart  = "tool_execution_start"
	AgentEventToolExecutionUpdate = "tool_execution_update"
	AgentEventToolExecutionEnd    = "tool_execution_end"
)

// AgentEvent 是 Agent 生命周期事件的封闭接口
type AgentEvent interface {
	// Type 返回事件对应的类型标识
	Type() string
	agentEvent()
}

type AgentStartEvent struct{}

func (AgentStartEvent) Type() string { return AgentEventAgentStart }
func (AgentStartEvent) agentEvent()  {}

type AgentEndEvent struct {
	Messages []Message
}

func (AgentEndEvent) Type() string { return AgentEventAgentEnd }
func (AgentEndEvent) agentEvent()  {}

type TurnStartEvent struct{}

func (TurnStartEvent) Type() string { return AgentEventTurnStart }
func (TurnStartEvent) agentEvent()  {}

type TurnEndEvent struct {
	Message     Message
	ToolResults []ToolResultMessage
}

func (TurnEndEvent) Type() string { return AgentEventTurnEnd }
func (TurnEndEvent) agentEvent()  {}

type MessageStartEvent struct {
	Message Message
}

func (MessageStartEvent) Type() string { return AgentEventMessageStart }
func (MessageStartEvent) agentEvent()  {}

type MessageUpdateEvent struct {
	Message               Message
	AssistantMessageEvent Event
}

func (MessageUpdateEvent) Type() string { return AgentEventMessageUpdate }
func (MessageUpdateEvent) agentEvent()  {}

type MessageEndEvent struct {
	Message Message
}

func (MessageEndEvent) Type() string { return AgentEventMessageEnd }
func (MessageEndEvent) agentEvent()  {}

type ToolExecutionStartEvent struct {
	ToolCallID string
	ToolName   string
	Arguments  map[string]any
}

func (ToolExecutionStartEvent) Type() string { return AgentEventToolExecutionStart }
func (ToolExecutionStartEvent) agentEvent()  {}

type ToolExecutionUpdateEvent struct {
	ToolCallID    string
	ToolName      string
	Arguments     map[string]any
	PartialResult ToolResult
}

func (ToolExecutionUpdateEvent) Type() string { return AgentEventToolExecutionUpdate }
func (ToolExecutionUpdateEvent) agentEvent()  {}

type ToolExecutionEndEvent struct {
	ToolCallID string
	ToolName   string
	Result     ToolResult
	IsError    bool
}

func (ToolExecutionEndEvent) Type() string { return AgentEventToolExecutionEnd }
func (ToolExecutionEndEvent) agentEvent()  {}

type AgentEventSink func(AgentEvent) error

type StreamFn func(ctx context.Context, model Model, c Context, opts Options) (<-chan Event, error)
