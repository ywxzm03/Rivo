package app

import (
	"testing"

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
	a.agent.AgentConfig.Model.ContextWindow = 0
	if usage := a.ContextUsage(); usage.Available || usage.ContextWindow != 0 {
		t.Fatalf("ContextUsage = %+v, want unavailable", usage)
	}
}
