package llm

import "context"

const (
	EventStart         = "start"
	EventTextStart     = "text_start"
	EventTextDelta     = "text_delta"
	EventTextEnd       = "text_end"
	EventThinkingStart = "thinking_start"
	EventThinkingDelta = "thinking_delta"
	EventThinkingEnd   = "thinking_end"
	EventToolCallStart = "toolcall_start"
	EventToolCallDelta = "toolcall_delta"
	EventToolCallEnd   = "toolcall_end"
	EventDone          = "done"
	EventError         = "error"
)

// AssistantMessageEvent 模型流事件，主要用于底层stream的返回消息的积攒，工具调用等；同时还会转化成AgentEvent供其消费者消费
type AssistantMessageEvent interface {
	Type() string
	assistantMessageEvent()
}

type AssistantMessageStartEvent struct {
	Partial *AssistantMessage
}

func (AssistantMessageStartEvent) Type() string           { return EventStart }
func (AssistantMessageStartEvent) assistantMessageEvent() {}

type AssistantMessageTextStartEvent struct {
	ContentIndex int
	Partial      *AssistantMessage
}

func (AssistantMessageTextStartEvent) Type() string           { return EventTextStart }
func (AssistantMessageTextStartEvent) assistantMessageEvent() {}

type AssistantMessageTextDeltaEvent struct {
	ContentIndex int
	Delta        string
	Partial      *AssistantMessage
}

func (AssistantMessageTextDeltaEvent) Type() string           { return EventTextDelta }
func (AssistantMessageTextDeltaEvent) assistantMessageEvent() {}

type AssistantMessageTextEndEvent struct {
	ContentIndex int
	Content      string
	Partial      *AssistantMessage
}

func (AssistantMessageTextEndEvent) Type() string           { return EventTextEnd }
func (AssistantMessageTextEndEvent) assistantMessageEvent() {}

type AssistantMessageThinkingStartEvent struct {
	ContentIndex int
	Partial      *AssistantMessage
}

func (AssistantMessageThinkingStartEvent) Type() string           { return EventThinkingStart }
func (AssistantMessageThinkingStartEvent) assistantMessageEvent() {}

type AssistantMessageThinkingDeltaEvent struct {
	ContentIndex int
	Delta        string
	Partial      *AssistantMessage
}

func (AssistantMessageThinkingDeltaEvent) Type() string           { return EventThinkingDelta }
func (AssistantMessageThinkingDeltaEvent) assistantMessageEvent() {}

type AssistantMessageThinkingEndEvent struct {
	ContentIndex int
	Content      string
	Partial      *AssistantMessage
}

func (AssistantMessageThinkingEndEvent) Type() string           { return EventThinkingEnd }
func (AssistantMessageThinkingEndEvent) assistantMessageEvent() {}

type AssistantMessageToolCallStartEvent struct {
	ContentIndex int
	ToolCall     ToolCall
	Partial      *AssistantMessage
}

func (AssistantMessageToolCallStartEvent) Type() string           { return EventToolCallStart }
func (AssistantMessageToolCallStartEvent) assistantMessageEvent() {}

type AssistantMessageToolCallDeltaEvent struct {
	ContentIndex int
	Delta        string
	Partial      *AssistantMessage
}

func (AssistantMessageToolCallDeltaEvent) Type() string           { return EventToolCallDelta }
func (AssistantMessageToolCallDeltaEvent) assistantMessageEvent() {}

type AssistantMessageToolCallEndEvent struct {
	ContentIndex int
	ToolCall     ToolCall
	Partial      *AssistantMessage
}

func (AssistantMessageToolCallEndEvent) Type() string           { return EventToolCallEnd }
func (AssistantMessageToolCallEndEvent) assistantMessageEvent() {}

type AssistantMessageDoneEvent struct {
	Message *AssistantMessage
	Partial *AssistantMessage
}

func (AssistantMessageDoneEvent) Type() string           { return EventDone }
func (AssistantMessageDoneEvent) assistantMessageEvent() {}

type AssistantMessageErrorEvent struct {
	Err     error
	Partial *AssistantMessage
}

func (AssistantMessageErrorEvent) Type() string           { return EventError }
func (AssistantMessageErrorEvent) assistantMessageEvent() {}

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
	AgentEventCompactionStart     = "compaction_start"
	AgentEventCompactionEnd       = "compaction_end"
)

// AgentEvent 的消费者主要是TUI渲染，自动压缩等Agent级事件
type AgentEvent interface {
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
	AssistantMessageEvent AssistantMessageEvent
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

// CompactionStartEvent 上下文压缩开始；Reason 取 auto（阈值触发）/ overflow（溢出恢复）/ manual（手动 /compact）
type CompactionStartEvent struct {
	Reason       string
	TokensBefore int
}

func (CompactionStartEvent) Type() string { return AgentEventCompactionStart }
func (CompactionStartEvent) agentEvent()  {}

// CompactionEndEvent 上下文压缩完成；KeptMessageCount 为保留未压缩的尾部消息数，
// 摘要消息本身经 CompactionEntry 回放产出，不发 message_end、不持久化为消息 entry
type CompactionEndEvent struct {
	Reason           string
	Summary          string
	KeptMessageCount int
	TokensBefore     int
	TokensAfter      int
	Usage            Usage
}

func (CompactionEndEvent) Type() string { return AgentEventCompactionEnd }
func (CompactionEndEvent) agentEvent()  {}

type AgentEventSink func(AgentEvent) error

type StreamFn func(ctx context.Context, model Model, c Context, opts Options) (<-chan AssistantMessageEvent, error)
