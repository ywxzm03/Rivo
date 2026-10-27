package llm

import (
	"encoding/json"
	"testing"
)

// 磁盘上的 usage 字段名是持久化契约，改名会让旧会话读不回来
func TestUsageJSONFieldNames(t *testing.T) {
	cost := &Cost{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Total: 10}
	data, err := json.Marshal(Usage{
		InputTokens:        1,
		OutputTokens:       2,
		CacheReadTokens:    3,
		CacheWriteTokens:   4,
		CacheWrite1HTokens: 5,
		ReasoningTokens:    6,
		TotalTokens:        7,
		Cost:               cost,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"inputTokens":1,"outputTokens":2,"cacheReadTokens":3,"cacheWriteTokens":4,"cacheWrite1hTokens":5,"reasoningTokens":6,"totalTokens":7,"cost":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"total":10}}`
	if string(data) != want {
		t.Fatalf("usage json = %s\nwant %s", data, want)
	}
}
