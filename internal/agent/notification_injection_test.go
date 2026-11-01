package agent

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 认领到的子任务通知只能进入模型上下文一次：落盘与追加由 commitMessages 统一负责
func TestRunLoopInjectsClaimedNotificationOnce(t *testing.T) {
	notification := llm.UserMessage{Content: []llm.Content{
		llm.TextContent{Text: "<task-notification>gin-demo done</task-notification>"},
	}}

	streamFn := func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
			Content: []llm.Content{llm.TextContent{Text: "done"}},
		}}
		close(ch)
		return ch, nil
	}

	a := New(Options{Config: llm.AgentConfig{Model: llm.Model{ID: "m1"}}, Context: llm.Context{}, Stream: streamFn})

	var committed [][]llm.Message
	a.hooks.CommitMessages = func(messages []llm.Message) ([]string, error) {
		committed = append(committed, messages)
		return nil, nil
	}

	acks := 0
	claimed := false
	a.hooks.PrepareRequest = func(_ context.Context, request PreparedRequest) (PreparedRequest, error) {
		if claimed {
			return request, nil
		}
		claimed = true
		request.ClaimNotifications = func() ([]llm.Message, func()) {
			return []llm.Message{notification}, func() { acks++ }
		}
		return request, nil
	}

	if _, err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if acks != 1 {
		t.Fatalf("ack count = %d, want 1", acks)
	}

	persisted := 0
	for _, batch := range committed {
		for _, message := range batch {
			if isNotification(message) {
				persisted++
			}
		}
	}
	if persisted != 1 {
		t.Fatalf("notification committed %d times, want 1", persisted)
	}

	inContext := 0
	for _, message := range a.ctx.Messages {
		if isNotification(message) {
			inContext++
		}
	}
	if inContext != 1 {
		t.Fatalf("notification appears %d times in context, want 1", inContext)
	}
}

func isNotification(message llm.Message) bool {
	user, ok := message.(llm.UserMessage)
	if !ok {
		return false
	}
	for _, content := range user.Content {
		if text, ok := content.(llm.TextContent); ok && text.Text == "<task-notification>gin-demo done</task-notification>" {
			return true
		}
	}
	return false
}
