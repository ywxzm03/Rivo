package tui

import (
	"fmt"
	"strings"
)

// statusLabelGap 是标签与数值列之间的最小间隔
const statusLabelGap = 4

// formatStatus 输出会话状态摘要，标签左对齐、数值从同一列开始
func formatStatus(msg SessionStatusMsg) string {
	contextWindow := "unavailable"
	if msg.Available && msg.ContextWindow > 0 {
		contextWindow = fmt.Sprintf("%.1f%% used (%s / %s)", float64(msg.Tokens)/float64(msg.ContextWindow)*100,
			formatStatusTokens(msg.Tokens), formatStatusTokens(msg.ContextWindow))
	}
	threadName := msg.ThreadName
	if threadName == "" {
		threadName = "(unnamed)"
	}
	rows := [][2]string{
		{"Model:", msg.Model},
		{"Provider:", msg.Provider},
		{"Context window:", contextWindow},
		{"Thread name:", threadName},
		{"Session:", msg.SessionID},
		{"Transcript:", msg.Transcript},
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row[0]))
	}
	var builder strings.Builder
	for i, row := range rows {
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(row[0])
		builder.WriteString(strings.Repeat(" ", width-len(row[0])+statusLabelGap))
		builder.WriteString(row[1])
	}
	return builder.String()
}

// formatStatusTokens 以 K/M 后缀输出 token 数，整值不带小数
func formatStatusTokens(tokens int) string {
	value, suffix := float64(tokens), ""
	switch {
	case tokens >= 1_000_000:
		value, suffix = float64(tokens)/1_000_000, "M"
	case tokens >= 1000:
		value, suffix = float64(tokens)/1000, "K"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0") + suffix
}
