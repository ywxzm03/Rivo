package compaction

import "github.com/ywxzm03/Rivo/internal/llm"

// CutPlan 描述一次压缩的切割位置：CutIndex 为保留尾部起点，
// IsSplitTurn 表示切点落在轮次中间，TurnStartIndex 指向被截断轮次的 user 消息
type CutPlan struct {
	CutIndex       int
	TurnStartIndex int
	IsSplitTurn    bool
}

// FindCutPoint 从最新消息向前累计 token，预算用尽处就近定位合法切割点：
// 切点只会落在 user/assistant 消息之前，落在工具结果上时向后滑动，绝不切断工具调用与其结果的配对；
// 保留尾部以 assistant 开头说明该轮的 user 消息被划入摘要区，按 split turn 记录轮次起点。
// 返回的 CutIndex 恒在 [start+1, len]，允许零保留；预算没被用完（历史比保留量还小）却仍需压缩时，
// 只保留最新一条消息以保证始终有历史可摘要
func FindCutPoint(messages []llm.Message, start, keepRecentTokens int) CutPlan {
	plan := CutPlan{CutIndex: len(messages), TurnStartIndex: -1}
	if start < 0 {
		start = 0
	}
	if start >= len(messages) {
		return plan
	}
	// 1.从最新消息向前累计，找到保留预算的边界
	total := 0
	budget := len(messages) - 1
	reached := false
	for budget >= start {
		total += MessageTokens(messages[budget])
		if total >= keepRecentTokens {
			reached = true
			break
		}
		budget--
	}
	// 2.定位切割点：预算内最早的消息即候选切点，预算没用完时退化为只保留最新一条
	cut := len(messages) - 1
	if reached {
		cut = budget
	}
	if cut < start+1 {
		cut = start + 1
	}
	// 3.切点不能落在工具结果上，向后滑动到下一个合法位置
	for cut < len(messages) && !isCutBoundary(messages[cut]) {
		cut++
	}
	if cut > len(messages) {
		cut = len(messages)
	}
	plan.CutIndex = cut
	// 4.保留尾部以 assistant 开头时定位轮次起点，供 split turn 双摘要使用
	if cut < len(messages) {
		if _, ok := messages[cut].(llm.UserMessage); !ok {
			for i := cut - 1; i >= start; i-- {
				if _, ok := messages[i].(llm.UserMessage); ok {
					plan.TurnStartIndex = i
					plan.IsSplitTurn = true
					break
				}
			}
		}
	}
	return plan
}

// isCutBoundary 判断 index 处是否为合法切割点：user/assistant 消息之前可以切
func isCutBoundary(msg llm.Message) bool {
	switch msg.(type) {
	case llm.UserMessage, llm.AssistantMessage:
		return true
	default:
		return false
	}
}
