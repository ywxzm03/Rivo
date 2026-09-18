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

type StreamFn func(ctx context.Context, model Model, c Context, opts Options) (<-chan Event, error)
