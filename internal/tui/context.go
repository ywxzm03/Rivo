package tui

import "fmt"

func formatContextUsage(msg ContextUsageMsg) string {
	if !msg.Available || msg.ContextWindow <= 0 {
		return "context: unavailable"
	}
	return fmt.Sprintf("context: %s/%s", formatContextTokens(msg.Tokens), formatContextTokens(msg.ContextWindow))
}

func formatContextTokens(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%.1f", float64(tokens))
	}
	if tokens < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(tokens)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
}
