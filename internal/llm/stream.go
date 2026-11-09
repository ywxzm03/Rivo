package llm

import (
	"context"
	"errors"
)

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

// AssistantMessageEvent 是模型流事件，用于累积底层 stream 返回的消息与工具调用，
// 同时会转换成 AgentEvent 供上层消费
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
	// Reason 区分 aborted（用户取消）与 error（真实失败）
	Reason  StopReason
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
	AgentEventWarning             = "warning"
)

// AgentEvent 是 Agent 级事件，消费者主要是 TUI 渲染与自动压缩
type AgentEvent interface {
	Type() string
	agentEvent()
}

type AgentStartEvent struct{}

func (AgentStartEvent) Type() string { return AgentEventAgentStart }
func (AgentStartEvent) agentEvent()  {}

type AgentEndEvent struct {
	Messages []Message
	Err      error
	// WillRetry 由编排层标注：本轮以可恢复的上下文溢出收场且即将续接重试
	WillRetry bool
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
	// SourceID 由宿主提交边界提供，为空表示尚未落盘
	SourceID string
	// Pending 区分尚未落盘的显示输出与成功提交的结束通知
	Pending bool
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

// WarningEvent 表示不阻止当前操作、但需要告知用户的降级状态
type WarningEvent struct {
	Message string
}

func (WarningEvent) Type() string { return AgentEventWarning }
func (WarningEvent) agentEvent()  {}

// CompactionStartEvent 上下文压缩开始；Reason 取 auto / overflow / manual
type CompactionStartEvent struct {
	Reason       string
	TokensBefore int
}

func (CompactionStartEvent) Type() string { return AgentEventCompactionStart }
func (CompactionStartEvent) agentEvent()  {}

// CompactionEndEvent 上下文压缩完成；Error 非空表示压缩失败，此时会话与内存上下文均未修改，
// 事件仅用于通知
type CompactionEndEvent struct {
	Reason           string
	Summary          string
	KeptMessageCount int
	TokensBefore     int
	TokensAfter      int
	Usage            Usage
	Error            string
}

func (CompactionEndEvent) Type() string { return AgentEventCompactionEnd }
func (CompactionEndEvent) agentEvent()  {}

type AgentEventSink func(AgentEvent) error

// StreamFn 返回由生产者关闭的事件通道；消费者取消后仍需排空通道
type StreamFn func(ctx context.Context, model Model, c Context, opts Options) (<-chan AssistantMessageEvent, error)

// SendEvent 投递一个可取消的事件：消费者已退出时返回 false
func SendEvent(ctx context.Context, events chan<- AssistantMessageEvent, event AssistantMessageEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// SendTerminal 投递终止事件；终止事件必须送达，不接受取消
func SendTerminal(events chan<- AssistantMessageEvent, event AssistantMessageEvent) {
	events <- event
}

// StopReasonForError 把 ctx 的取消/超时归为 aborted，其余视为真实错误
func StopReasonForError(ctx context.Context, err error) StopReason {
	if ctx != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return StopReasonAborted
	}
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return StopReasonAborted
	}
	return StopReasonError
}

// CloneMessage 复制消息结构与内容切片，避免消费者读到生产中的消息
func CloneMessage(message *AssistantMessage) *AssistantMessage {
	if message == nil {
		return nil
	}
	clone := *message
	clone.Content = append([]Content(nil), message.Content...)
	return &clone
}

// EventPartial 返回事件携带的 partial，无 partial 的事件返回 nil
func EventPartial(event AssistantMessageEvent) *AssistantMessage {
	switch event := event.(type) {
	case AssistantMessageStartEvent:
		return event.Partial
	case AssistantMessageTextStartEvent:
		return event.Partial
	case AssistantMessageTextDeltaEvent:
		return event.Partial
	case AssistantMessageTextEndEvent:
		return event.Partial
	case AssistantMessageThinkingStartEvent:
		return event.Partial
	case AssistantMessageThinkingDeltaEvent:
		return event.Partial
	case AssistantMessageThinkingEndEvent:
		return event.Partial
	case AssistantMessageToolCallStartEvent:
		return event.Partial
	case AssistantMessageToolCallDeltaEvent:
		return event.Partial
	case AssistantMessageToolCallEndEvent:
		return event.Partial
	case AssistantMessageDoneEvent:
		return event.Partial
	case AssistantMessageErrorEvent:
		return event.Partial
	}
	return nil
}

// WithPartial 把 partial 挂到事件上；不支持 partial 的事件原样返回
func WithPartial(event AssistantMessageEvent, partial *AssistantMessage) AssistantMessageEvent {
	switch event := event.(type) {
	case AssistantMessageStartEvent:
		event.Partial = partial
		return event
	case AssistantMessageTextStartEvent:
		event.Partial = partial
		return event
	case AssistantMessageTextDeltaEvent:
		event.Partial = partial
		return event
	case AssistantMessageTextEndEvent:
		event.Partial = partial
		return event
	case AssistantMessageThinkingStartEvent:
		event.Partial = partial
		return event
	case AssistantMessageThinkingDeltaEvent:
		event.Partial = partial
		return event
	case AssistantMessageThinkingEndEvent:
		event.Partial = partial
		return event
	case AssistantMessageToolCallStartEvent:
		event.Partial = partial
		return event
	case AssistantMessageToolCallDeltaEvent:
		event.Partial = partial
		return event
	case AssistantMessageToolCallEndEvent:
		event.Partial = partial
		return event
	case AssistantMessageDoneEvent:
		event.Partial = partial
		return event
	case AssistantMessageErrorEvent:
		event.Partial = partial
		return event
	default:
		return event
	}
}

// AppendText 追加文本增量；空 slot 初始化为文本，其它类型保持不动
func AppendText(message *AssistantMessage, index int, delta string) {
	if index < 0 || index >= len(message.Content) {
		return
	}
	if message.Content[index] == nil {
		message.Content[index] = TextContent{Text: delta}
		return
	}
	text, ok := message.Content[index].(TextContent)
	if !ok {
		return
	}
	text.Text += delta
	message.Content[index] = text
}

// AppendThinking 追加思考增量；空 slot 初始化为思考，其它类型保持不动
func AppendThinking(message *AssistantMessage, index int, delta string) {
	if index < 0 || index >= len(message.Content) {
		return
	}
	if message.Content[index] == nil {
		message.Content[index] = ThinkingContent{Thinking: delta}
		return
	}
	thinking, ok := message.Content[index].(ThinkingContent)
	if !ok {
		return
	}
	thinking.Thinking += delta
	message.Content[index] = thinking
}

// TextAt 返回指定 slot 的文本，slot 不是文本时返回空串
func TextAt(message *AssistantMessage, index int) string {
	if message == nil || index < 0 || index >= len(message.Content) {
		return ""
	}
	text, _ := message.Content[index].(TextContent)
	return text.Text
}

// ThinkingAt 返回指定 slot 的思考内容，slot 不是思考时返回空串
func ThinkingAt(message *AssistantMessage, index int) string {
	if message == nil || index < 0 || index >= len(message.Content) {
		return ""
	}
	thinking, _ := message.Content[index].(ThinkingContent)
	return thinking.Thinking
}
