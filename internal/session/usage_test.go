package session

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestUsageTotalsIncludesAllPersistedUsage(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(llm.AssistantMessage{Usage: llm.Usage{
		InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40, TotalTokens: 100,
		Cost: &llm.Cost{Input: 0.1, Output: 0.2, CacheRead: 0.3, CacheWrite: 0.4, Total: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	toolUsage := llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3, Cost: &llm.Cost{Total: 0.2}}
	if err := m.AppendMessage(llm.ToolResultMessage{Usage: &toolUsage}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendUsage("cache_warm", "openai", "gpt", llm.Usage{CacheReadTokens: 7, TotalTokens: 7, Cost: &llm.Cost{Total: 0.3}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendCompaction("summary", "", 100, nil, &llm.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5, Cost: &llm.Cost{Total: 0.4}}); err != nil {
		t.Fatal(err)
	}

	got := m.UsageTotals()
	want := UsageTotals{
		InputTokens: 13, OutputTokens: 25, CacheReadTokens: 37, CacheWriteTokens: 40, TotalTokens: 115,
		Cost: llm.Cost{Input: 0.1, Output: 0.2, CacheRead: 0.3, CacheWrite: 0.4, Total: 1.9},
	}
	if got != want {
		t.Fatalf("UsageTotals = %+v, want %+v", got, want)
	}
}

func TestUsageTotalsCountsUnknownCosts(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(llm.AssistantMessage{Usage: llm.Usage{InputTokens: 1, TotalTokens: 1}}); err != nil {
		t.Fatal(err)
	}
	got := m.UsageTotals()
	if got.UnknownCostEntries != 1 || got.Cost.Total != 0 {
		t.Fatalf("UsageTotals = %+v, want one unknown entry and zero known cost", got)
	}
}
