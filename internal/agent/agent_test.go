package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// fakeTool 是测试用的工具，记录每次收到的参数
type fakeTool struct {
	name      string
	arguments []map[string]any
}

func (t *fakeTool) Name() string               { return t.name }
func (t *fakeTool) Description() string        { return "" }
func (t *fakeTool) Parameters() map[string]any { return nil }

func (t *fakeTool) Execute(_ context.Context, args map[string]any) (llm.ToolResult, error) {
	t.arguments = append(t.arguments, args)
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "pong"}}}, nil
}

var _ llm.Tool = (*fakeTool)(nil)

// doneStreamFn 造一个按调用次数依次返回预设回复的假 StreamFn
func doneStreamFn(calls *int, replies ...llm.AssistantMessage) llm.StreamFn {
	return func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.Event, error) {
		i := *calls
		*calls++
		ch := make(chan llm.Event, 1)
		reply := replies[min(i, len(replies)-1)]
		ch <- llm.Event{Type: llm.EventDone, Message: &reply}
		close(ch)
		return ch, nil
	}
}

func TestAgentRunToolLoop(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{
			Content:    []llm.Content{llm.ToolCall{ID: "c1", Name: "ping", Arguments: map[string]any{}}},
			StopReason: llm.StopReasonToolUse,
		},
		llm.AssistantMessage{
			Content:    []llm.Content{llm.TextContent{Text: "done"}},
			StopReason: llm.StopReasonStop,
		},
	)

	ping := &fakeTool{name: "ping"}
	a := NewAgent(
		llm.AgentConfig{Model: llm.Model{ID: "m1"}},
		llm.Context{Tools: []llm.Tool{ping}},
		streamFn,
		nil,
	)

	reply, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("StreamFn 调用 %d 次, want 2", calls)
	}
	if len(ping.arguments) != 1 {
		t.Errorf("工具被调用 %d 次, want 1（派发应走 AgentContext.Tools）", len(ping.arguments))
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

func TestAgentRunUnknownTool(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{
			Content:    []llm.Content{llm.ToolCall{ID: "c1", Name: "nope", Arguments: map[string]any{}}},
			StopReason: llm.StopReasonToolUse,
		},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}},
	)

	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, nil)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	if got := len(a.AgentContext.Messages); got != 4 {
		t.Fatalf("历史消息 %d 条, want 4", got)
	}
	tr, ok := a.AgentContext.Messages[2].(llm.ToolResultMessage)
	if !ok {
		t.Fatalf("Messages[2] = %T, want ToolResultMessage", a.AgentContext.Messages[2])
	}
	text, _ := tr.Content[0].(llm.TextContent)
	if !strings.Contains(text.Text, "tool error:") {
		t.Errorf("工具结果 = %q, want 含 tool error:", text.Text)
	}
}

func TestAgentRunStreamError(t *testing.T) {
	sentinel := errors.New("boom")
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.Event, error) {
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
