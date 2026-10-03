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
// 其后没有合法切点时回退到最后一个合法切点，预算未用完时切在最早合法切点——任何情况下都不产生零保留，
// 摘要区为空（CutIndex == start）时由调用方放弃本次压缩。
// 保留尾部以 assistant 开头说明该轮的 user 消息被划入摘要区，按 split turn 记录轮次起点
func FindCutPoint(messages []llm.Message, start, keepRecentTokens int) CutPlan {
	plan := CutPlan{CutIndex: start, TurnStartIndex: -1}
	if start < 0 {
		start = 0
	}
	if start >= len(messages) {
		return plan
	}
	// 1.收集区间内的合法切点：user/assistant 消息之前可以切
	var cuts []int
	for i := start; i < len(messages); i++ {
		if isCutBoundary(messages[i]) {
			cuts = append(cuts, i)
		}
	}
	if len(cuts) == 0 {
		return plan
	}
	// 2.从最新消息向前累计，预算用尽处就近取其后的合法切点；
	// 其后没有切点（尾部全是工具结果）时回退到最后一个合法切点，多保留而不清空保留区
	total := 0
	for i := len(messages) - 1; i >= start; i-- {
		total += MessageTokens(messages[i])
		if total >= keepRecentTokens {
			cut := cuts[len(cuts)-1]
			for _, c := range cuts {
				if c >= i {
					cut = c
					break
				}
			}
			plan.CutIndex = cut
			markSplitTurn(&plan, messages, start, cut)
			return plan
		}
	}
	// 3.预算未用完说明全部历史都在保留量内：切在最早合法切点上，摘要区为空时调用方放弃压缩
	plan.CutIndex = cuts[0]
	markSplitTurn(&plan, messages, start, cuts[0])
	return plan
}

// markSplitTurn 保留尾部以 assistant 开头时定位被截断轮次的 user 消息
func markSplitTurn(plan *CutPlan, messages []llm.Message, start, cut int) {
	if cut >= len(messages) {
		return
	}
	if _, ok := messages[cut].(llm.UserMessage); ok {
		return
	}
	for i := cut - 1; i >= start; i-- {
		if _, ok := messages[i].(llm.UserMessage); ok {
			plan.TurnStartIndex = i
			plan.IsSplitTurn = true
			return
		}
	}
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
