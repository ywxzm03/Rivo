package codemode

import "github.com/ywxzm03/Rivo/internal/llm"

// addUsage 在 owner 接收 completion 时累计一次，包括未等待但已完成的工具
func (e *execution) addUsage(usage *llm.Usage) {
	if usage == nil {
		return
	}
	if e.calls.usage == nil {
		e.calls.usage = &llm.Usage{}
	}
	total := e.calls.usage
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheReadTokens += usage.CacheReadTokens
	total.CacheWriteTokens += usage.CacheWriteTokens
	total.CacheWrite1HTokens += usage.CacheWrite1HTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
	if usage.Cost != nil {
		if total.Cost == nil {
			total.Cost = &llm.Cost{}
		}
		total.Cost.Input += usage.Cost.Input
		total.Cost.Output += usage.Cost.Output
		total.Cost.CacheRead += usage.Cost.CacheRead
		total.Cost.CacheWrite += usage.Cost.CacheWrite
		total.Cost.Total += usage.Cost.Total
	}
}
