package llm

// NormalizeUsage 统一 provider 返回的 token 分项口径。
// CacheWrite1HTokens 是 cache write 的细分项；当 provider 只返回该细分项时，
// CacheWriteTokens 也补齐它，避免上下文 token 与 total 漏计。
func NormalizeUsage(prompt, completion, total, cacheRead, cacheWrite, cacheWrite1h, reasoning int) Usage {
	cacheWrite = max(cacheWrite, cacheWrite1h)
	input := max(0, prompt-cacheRead-cacheWrite)
	if total == 0 {
		total = input + completion + cacheRead + cacheWrite
	}
	return Usage{
		InputTokens:        input,
		OutputTokens:       completion,
		CacheReadTokens:    cacheRead,
		CacheWriteTokens:   cacheWrite,
		CacheWrite1HTokens: cacheWrite1h,
		ReasoningTokens:    reasoning,
		TotalTokens:        total,
	}
}
