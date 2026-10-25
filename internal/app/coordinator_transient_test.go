package app

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

func TestCoordinatorTransientMessagesAreRequestOnly(t *testing.T) {
	sm := session.New("/work", t.TempDir())
	coord := NewCoordinator(sm, nil)
	used := false
	coord.SetTransientMessages(func() []llm.Message {
		if used {
			return nil
		}
		used = true
		return []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "<task-notification>{}</task-notification>"}}}}
	})
	request, err := coord.PrepareRequest(context.Background(), agent.PreparedRequest{Context: llm.Context{Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "input"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Context.Messages) != 1 || request.Context.Messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text != "<task-notification>{}</task-notification>" {
		t.Fatalf("request messages = %#v, want only transient message from persisted projection", request.Context.Messages)
	}
	if len(sm.BuildContext().Messages()) != 0 {
		t.Fatal("transient task notification was persisted into the parent session")
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
