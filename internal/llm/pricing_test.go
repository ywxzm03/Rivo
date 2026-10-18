package llm

import (
	"math"
	"testing"
)

func TestPriceUsageOpenAIStandardAndLongContext(t *testing.T) {
	standard := PriceUsage(Model{Provider: "openai", ID: "gpt-6.1-sol"}, Usage{
		InputTokens: 80_000, OutputTokens: 1_000_000, CacheReadTokens: 80_000, CacheWriteTokens: 80_000,
	})
	if standard == nil {
		t.Fatal("standard cost = nil")
	}
	assertCost(t, *standard, Cost{Input: 0.16, Output: 10, CacheRead: 0.008, CacheWrite: 0.2, Total: 10.368})

	long := PriceUsage(Model{Provider: "openai", ID: "gpt-6.1-sol"}, Usage{
		InputTokens: 272_001, OutputTokens: 1_000_000,
	})
	if long == nil {
		t.Fatal("long-context cost = nil")
	}
	assertCost(t, *long, Cost{Input: 1.088004, Output: 15, Total: 16.088004})
}

func TestPriceUsageDeepSeekUsesFixedOffPeakUSD(t *testing.T) {
	cost := PriceUsage(Model{Provider: "deepseek", ID: "deepseek-flash"}, Usage{
		InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000,
	})
	if cost == nil {
		t.Fatal("cost = nil")
	}
	assertCost(t, *cost, Cost{Input: 0.14, Output: 0.56, CacheRead: 0.0028, Total: 0.7028})

	pro := PriceUsage(Model{Provider: "deepseek", ID: "deepseek-v4-pro"}, Usage{
		InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000,
	})
	if pro == nil {
		t.Fatal("pro cost = nil")
	}
	assertCost(t, *pro, Cost{Input: 0.63, Output: 1.89, CacheRead: 0.021, Total: 2.541})
}

func TestPriceUsageUnknownModelReturnsNil(t *testing.T) {
	if cost := PriceUsage(Model{Provider: "custom", ID: "model"}, Usage{InputTokens: 1}); cost != nil {
		t.Fatalf("cost = %+v, want nil", cost)
	}
}

func assertCost(t *testing.T, got, want Cost) {
	t.Helper()
	values := [][2]float64{
		{got.Input, want.Input}, {got.Output, want.Output}, {got.CacheRead, want.CacheRead},
		{got.CacheWrite, want.CacheWrite}, {got.Total, want.Total},
	}
	for i, pair := range values {
		if math.Abs(pair[0]-pair[1]) > 1e-9 {
			t.Fatalf("cost field %d = %.12f, want %.12f", i, pair[0], pair[1])
		}
	}
}
