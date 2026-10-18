package session

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestUsageTotalsIncludesAllPersistedUsage(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(llm.AssistantMessage{Usage: llm.Usage{
		InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40, TotalTokens: 100,
	}}); err != nil {
		t.Fatal(err)
	}
	toolUsage := llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}
	if err := m.AppendMessage(llm.ToolResultMessage{Usage: &toolUsage}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendUsage("cache_warm", "openai", "gpt", llm.Usage{CacheReadTokens: 7, TotalTokens: 7}, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendCompaction("summary", "", 100, nil, &llm.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}); err != nil {
		t.Fatal(err)
	}

	got := m.UsageTotals()
	want := UsageTotals{InputTokens: 13, OutputTokens: 25, CacheReadTokens: 37, CacheWriteTokens: 40, TotalTokens: 115}
	if got != want {
		t.Fatalf("UsageTotals = %+v, want %+v", got, want)
	}
}
