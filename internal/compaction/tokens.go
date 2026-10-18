// Package compaction 提供上下文压缩的纯逻辑：阈值判断、切割点定位、对话序列化、
// 摘要提示词与摘要调用；会话 I/O 由 session 层负责，压缩结果由 agent 回插到内存上下文
package compaction

import (
	"encoding/json"
	"fmt"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// imageTokenEstimate 图片按固定 token 数估算
const imageTokenEstimate = 1200

// EstimateTokens 按 chars/4 粗估文本 token 数；CJK 场景会低估，仅在没有真实 usage 时兜底
func EstimateTokens(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// contentTokens 估算单个内容块：文本按 chars/4，图片按固定值，工具调用按参数 JSON 估算
func contentTokens(content llm.Content) int {
	switch c := content.(type) {
	case llm.TextContent:
		return EstimateTokens(c.Text)
	case llm.ImageContent:
		return imageTokenEstimate
	case llm.ToolCall:
		data, err := json.Marshal(c.Arguments)
		if err != nil {
			// 不可序列化的参数以 fmt.Sprint 兜底，避免静默丢数据
			return EstimateTokens(c.Name) + EstimateTokens(fmt.Sprint(c.Arguments))
		}
		return EstimateTokens(c.Name) + EstimateTokens(string(data))
	case llm.ThinkingContent:
		return EstimateTokens(c.Thinking)
	default:
		return 0
	}
}

// MessageTokens 估算单条消息的 token 数
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
		// system prompt 分节文本计入上下文，各节按文本长度估算
		for _, text := range m.Sections {
			total += EstimateTokens(text)
		}
	}
	return total
}

// ContextTokens 估算当前上下文的 token 数：以最后一条有效 assistant 的完整 usage 为基准，
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

// EstimateContextTokens 纯消息体估算全部消息的 token 数，不使用任何真实 usage；
// 压缩后保留尾部的 usage 反映的是压缩前的大上下文，属于陈旧数据，此时应改用本函数
func EstimateContextTokens(messages []llm.Message) int {
	total := 0
	for _, msg := range messages {
		total += MessageTokens(msg)
	}
	return total
}

// ShouldCompact 判断是否达到压缩阈值：上下文占用超过窗口减去预留量；
// 窗口未知时不触发，预留量大于窗口时阈值收敛到 0，避免任意非空上下文都触发
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
