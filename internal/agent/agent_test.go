package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestAgentRunToolLoop(t *testing.T) {
	calls := 0
	streamFn := func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.Event, error) {
		calls++
		ch := make(chan llm.Event, 1)
		if calls == 1 {
			ch <- llm.Event{Type: llm.EventDone, Message: &llm.AssistantMessage{
				Content:    []llm.Content{llm.ToolCall{ID: "c1", Name: "ping", Arguments: map[string]any{}}},
				StopReason: llm.StopReasonToolUse,
			}}
		} else {
			ch <- llm.Event{Type: llm.EventDone, Message: &llm.AssistantMessage{
				Content:    []llm.Content{llm.TextContent{Text: "done"}},
				StopReason: llm.StopReasonStop,
			}}
		}
		close(ch)
		return ch, nil
	}
	var executed []string
	a := NewAgent(
		llm.AgentConfig{Model: llm.Model{ID: "m1"}},
		llm.Context{},
		streamFn,
		func(ctx context.Context, name string, args map[string]any) (string, error) {
			executed = append(executed, name)
			return "pong", nil
		},
	)

	reply, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("StreamFn 调用 %d 次, want 2", calls)
	}
	if len(executed) != 1 || executed[0] != "ping" {
		t.Errorf("executed = %v", executed)
	}
	txt, ok := reply.Content[0].(llm.TextContent)
	if !ok || txt.Text != "done" {
		t.Errorf("reply.Content = %+v", reply.Content)
	}

	if got := len(a.AgentContext.Messages); got != 4 {
		t.Errorf("历史消息 %d 条, want 4", got)
	}
	if _, ok := a.AgentContext.Messages[0].(llm.UserMessage); !ok {
		t.Errorf("Messages[0] = %T, want UserMessage", a.AgentContext.Messages[0])
	}
}

func TestAgentRunStreamError(t *testing.T) {
	sentinel := errors.New("boom")
	streamFn := func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.Event, error) {
		ch := make(chan llm.Event, 1)
		ch <- llm.Event{Type: llm.EventError, Err: sentinel}
		close(ch)
		return ch, nil
	}
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, nil)
	if _, err := a.Run(context.Background(), "hi"); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}
