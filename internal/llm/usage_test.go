package llm

import "testing"

func TestNormalizeUsageIncludesCacheWrite1HInContextAccounting(t *testing.T) {
	usage := NormalizeUsage(10, 2, 0, 1, 0, 3, 1)
	want := Usage{InputTokens: 6, OutputTokens: 2, CacheReadTokens: 1, CacheWriteTokens: 3, CacheWrite1HTokens: 3, ReasoningTokens: 1, TotalTokens: 12}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}

	if got := (Usage{InputTokens: 6, OutputTokens: 2, CacheReadTokens: 1, CacheWrite1HTokens: 3}).ContextTokens(); got != 12 {
		t.Fatalf("ContextTokens = %d, want 12", got)
	}
}
