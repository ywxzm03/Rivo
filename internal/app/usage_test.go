package app

import (
	"context"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestContextUsageUsesNormalizedTotal(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendMessage(llm.AssistantMessage{
		Provider: a.CurrentModel().Provider,
		Model:    a.CurrentModel().ID,
		Usage:    llm.Usage{InputTokens: 80, OutputTokens: 20, CacheReadTokens: 10, TotalTokens: 110},
	}); err != nil {
		t.Fatal(err)
	}

	usage := a.ContextUsage()
	if !usage.Available || usage.ContextWindow != a.CurrentModel().ContextWindow || usage.Tokens != 110 {
		t.Fatalf("ContextUsage = %+v, want 110/%d", usage, a.CurrentModel().ContextWindow)
	}
}

func TestContextUsageUnavailableWithoutWindow(t *testing.T) {
	a := operationApp(t, nil, nil)
	model := a.agent.Config().Model
	model.ContextWindow = 0
	a.agent.SetModel(model)
	if usage := a.ContextUsage(); usage.Available || usage.ContextWindow != 0 {
		t.Fatalf("ContextUsage = %+v, want unavailable", usage)
	}
}

func TestContextUsageDoesNotReuseAssistantUsageWithoutModelAnchor(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendMessage(llm.AssistantMessage{Usage: llm.Usage{TotalTokens: 900}}); err != nil {
		t.Fatal(err)
	}

	usage := a.ContextUsage()
	if !usage.Available || usage.Tokens >= 900 {
		t.Fatalf("ContextUsage = %+v, want estimated usage instead of unanchored provider total", usage)
	}
}

func TestContextUsageWaitsForActiveOperationState(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	a := operationApp(t, func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		close(started)
		<-release
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}}
		close(events)
		return events, nil
	}, nil)
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	finished := make(chan struct{})
	go func() {
		a.ContextUsage()
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("ContextUsage read active operation state")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	if result := op.Wait(); result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("ContextUsage did not resume after operation completion")
	}
}
