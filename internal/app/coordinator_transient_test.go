package app

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

func TestCoordinatorExposesNotificationClaimer(t *testing.T) {
	sm := session.New("/work", t.TempDir())
	coord := NewCoordinator(sm, nil)
	message := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "<task-notification>{}</task-notification>"}}}
	acked := false
	coord.SetNotificationClaimer(func() ([]llm.Message, func()) {
		return []llm.Message{message}, func() { acked = true }
	})
	request, err := coord.PrepareRequest(context.Background(), agent.PreparedRequest{Context: llm.Context{Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "input"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	// 通知不再挂进请求上下文,由 agent 认领后落盘
	if len(request.Context.Messages) != 0 {
		t.Fatalf("request messages = %#v, want the transient attachment to stay empty", request.Context.Messages)
	}
	if request.ClaimNotifications == nil {
		t.Fatal("ClaimNotifications not installed on the prepared request")
	}
	messages, ack := request.ClaimNotifications()
	if len(messages) != 1 || ack == nil {
		t.Fatalf("claimed = %#v, want one message and an ack", messages)
	}
	ack()
	if !acked {
		t.Fatal("ack callback did not run")
	}
}

func TestCoordinatorPrepareRequestRunsTaskEvictor(t *testing.T) {
	sm := session.New("/work", t.TempDir())
	coord := NewCoordinator(sm, nil)
	calls := 0
	coord.SetTaskEvictor(func() int {
		calls++
		return 1
	})
	request := agent.PreparedRequest{Context: llm.Context{Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "input"}}}}}}
	if _, err := coord.PrepareRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := coord.PrepareRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	// 每个请求边界都要回收一次,不能只在首次请求时生效
	if calls != 2 {
		t.Fatalf("evictor calls = %d, want one per request", calls)
	}
}
