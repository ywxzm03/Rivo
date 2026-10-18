package session

import "github.com/ywxzm03/Rivo/internal/llm"

// UsageTotals 汇总会话中所有持久化 entry 的 token usage。
type UsageTotals struct {
	InputTokens        int
	OutputTokens       int
	CacheReadTokens    int
	CacheWriteTokens   int
	CacheWrite1HTokens int
	ReasoningTokens    int
	TotalTokens        int
}

// Add 累加一条 usage；reasoning 和 cacheWrite1h 是其它字段的子集，不重复计入 total。
func (t *UsageTotals) Add(usage llm.Usage) {
	t.InputTokens += usage.InputTokens
	t.OutputTokens += usage.OutputTokens
	t.CacheReadTokens += usage.CacheReadTokens
	t.CacheWriteTokens += usage.CacheWriteTokens
	t.CacheWrite1HTokens += usage.CacheWrite1HTokens
	t.ReasoningTokens += usage.ReasoningTokens
	t.TotalTokens += usage.ContextTokens()
}

// UsageTotals 汇总全部 session entries，包括已压缩历史和非 active branch。
func (m *SessionManager) UsageTotals() UsageTotals {
	var totals UsageTotals
	for _, entry := range m.entries {
		switch entry := entry.(type) {
		case *MessageEntry:
			switch message := entry.Message.(type) {
			case llm.AssistantMessage:
				totals.Add(message.Usage)
			case llm.ToolResultMessage:
				if message.Usage != nil {
					totals.Add(*message.Usage)
				}
			}
		case *UsageEntry:
			totals.Add(usageFromWire(entry.Usage))
		case *CompactionEntry:
			if entry.Usage != nil {
				totals.Add(usageFromWire(*entry.Usage))
			}
		}
	}
	return totals
}

func usageFromWire(usage Usage) llm.Usage {
	return llm.Usage{
		InputTokens:        usage.InputTokens,
		OutputTokens:       usage.OutputTokens,
		CacheReadTokens:    usage.CacheReadTokens,
		CacheWriteTokens:   usage.CacheWriteTokens,
		CacheWrite1HTokens: usage.CacheWrite1HTokens,
		ReasoningTokens:    usage.ReasoningTokens,
		TotalTokens:        usage.TotalTokens,
	}
}
