// Package compaction 提供上下文压缩的纯逻辑：阈值判断、切割点定位、序列化与摘要调用
package compaction

import (
	"encoding/json"
	"fmt"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const imageTokenEstimate = 1200

// EstimateTokens 按 chars/4 粗估文本 token 数；CJK 场景会低估，仅在没有真实 usage 时兜底
func EstimateTokens(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

func contentTokens(content llm.Content) int {
	switch c := content.(type) {
	case llm.TextContent:
		return EstimateTokens(c.Text)
	case llm.ImageContent:
		return imageTokenEstimate
	case llm.ToolCall:
		data, err := json.Marshal(c.Arguments)
		if err != nil {
			return EstimateTokens(c.Name) + EstimateTokens(fmt.Sprint(c.Arguments))
		}
		return EstimateTokens(c.Name) + EstimateTokens(string(data))
	case llm.ThinkingContent:
		return EstimateTokens(c.Thinking)
	default:
		return 0
	}
}

func MessageTokens(msg llm.Message) int {
	total := 0
	switch m := msg.(type) {
	case llm.UserMessage:
		for _, c := range m.Content {
			total += contentTokens(c)
		}
	case llm.AssistantMessage:
		for _, c := range m.Content {
			total += contentTokens(c)
		}
	case llm.ToolResultMessage:
		for _, c := range m.Content {
			total += contentTokens(c)
		}
	case llm.SystemMessage:
		for _, text := range m.Sections {
			total += EstimateTokens(text)
		}
	}
	return total
}

// ContextTokens 以最后一条有效 assistant 的完整 usage 为基准估算上下文 token 数，
// 其后新增的消息按 chars/4 估算；usage 缺失时回退到全量估算
func ContextTokens(messages []llm.Message) int {
	last := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if assistant, ok := messages[i].(llm.AssistantMessage); ok && assistant.StopReason != llm.StopReasonError && assistant.StopReason != llm.StopReasonAborted && assistant.Usage.ContextTokens() > 0 {
			last = i
			break
		}
	}
	if last < 0 {
		return EstimateContextTokens(messages)
	}
	base := messages[last].(llm.AssistantMessage).Usage.ContextTokens()
	for _, msg := range messages[last+1:] {
		base += MessageTokens(msg)
	}
	return base
}

// EstimateContextTokens 纯消息体估算全部消息的 token 数，不使用真实 usage；
// 压缩后保留尾部的 usage 属于陈旧数据，此时应改用本函数
func EstimateContextTokens(messages []llm.Message) int {
	total := 0
	for _, msg := range messages {
		total += MessageTokens(msg)
	}
	return total
}

// ShouldCompact 判断是否达到压缩阈值；窗口未知时不触发，预留量大于窗口时阈值收敛到 0
func ShouldCompact(contextTokens, contextWindow, reserveTokens int) bool {
	if contextWindow <= 0 {
		return false
	}
	threshold := contextWindow - reserveTokens
	if threshold < 0 {
		threshold = 0
	}
	return contextTokens > threshold
}
