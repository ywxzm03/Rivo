package agent

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// Options 必须一次性装配全部运行参数，不能留下需要外部补写的字段
func TestNewAgentFromOptions(t *testing.T) {
	cfg := llm.AgentConfig{Model: llm.Model{ID: "gpt-5.6", Provider: "openai"}}
	ctx := llm.Context{Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hi"}}}}}
	streamFn := func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		return nil, nil
	}
	events := 0
	a := New(Options{
		Config:     cfg,
		Context:    ctx,
		Stream:     streamFn,
		Events:     func(llm.AgentEvent) error { events++; return nil },
		WorkingDir: "/tmp/work",
		MaxTurns:   7,
		ForkChild:  true,
		TaskID:     "agent-1",
	})
	if a.Config().Model.ID != "gpt-5.6" || a.Config().Model.Provider != "openai" {
		t.Errorf("Config() = %+v, want the model from options", a.Config())
	}
	if len(a.Context().Messages) != 1 {
		t.Errorf("Context().Messages = %d, want 1", len(a.Context().Messages))
	}
	if a.WorkingDir() != "/tmp/work" || a.MaxTurns() != 7 || !a.ForkChild() {
		t.Errorf("runtime fields = %q/%d/%v, want values from options",
			a.WorkingDir(), a.MaxTurns(), a.ForkChild())
	}
	if err := a.emit(llm.WarningEvent{Message: "probe"}); err != nil {
		t.Fatalf("emit() error = %v", err)
	}
	if events != 1 {
		t.Errorf("events = %d, want 1 delivered through the sink from options", events)
	}
}

// Reconfigure 必须能替换模型与流函数，同时保留队列等运行中状态
func TestReconfigureReplacesModelAndStream(t *testing.T) {
	a := New(Options{Config: llm.AgentConfig{Model: llm.Model{ID: "old"}}})
	if err := a.Enqueue(QueueSteering, PendingPayload{Text: "keep me"}); err != nil {
		t.Fatal(err)
	}

	streamFn := func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		return nil, nil
	}
	a.Reconfigure(llm.AgentConfig{Model: llm.Model{ID: "new"}}, streamFn)
	if a.Config().Model.ID != "new" {
		t.Errorf("Config().Model.ID = %q, want new", a.Config().Model.ID)
	}
	if a.Stream() == nil {
		t.Error("Stream() = nil, want the replaced stream function")
	}
	if a.Len(QueueSteering) != 1 {
		t.Errorf("Len(Steering) = %d, want queued payload preserved", a.Len(QueueSteering))
	}
}
