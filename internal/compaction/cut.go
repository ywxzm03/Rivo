package compaction

import "github.com/ywxzm03/Rivo/internal/llm"

// CutPlan 描述一次压缩的切割位置：CutIndex 为保留尾部起点，
// IsSplitTurn 表示切点落在轮次中间，TurnStartIndex 指向被截断轮次的 user entry
type CutPlan struct {
	CutIndex       int
	TurnStartIndex int
	IsSplitTurn    bool
}

// FindCutPoint 从最新 entry 向前累计 token，预算用尽处就近定位合法切割点：
// 切点只会落在 user/assistant entry 之前，保留尾部以 assistant 开头时按 split turn 记录轮次起点
func FindCutPoint(entries []ContextEntry, start, keepRecentTokens int) CutPlan {
	plan := CutPlan{CutIndex: start, TurnStartIndex: -1}
	if start < 0 {
		start = 0
	}
	if start >= len(entries) {
		return plan
	}
	// 1.收集区间内的合法切点
	var cuts []int
	for i := start; i < len(entries); i++ {
		if isCutBoundary(entries[i]) {
			cuts = append(cuts, i)
		}
	}
	if len(cuts) == 0 {
		return plan
	}
	// 2.从最新 entry 向前累计，预算用尽处就近取其后的合法切点
	total := 0
	for i := len(entries) - 1; i >= start; i-- {
		total += entryTokens(entries[i])
		if total >= keepRecentTokens {
			cut := cuts[len(cuts)-1]
			for _, candidate := range cuts {
				if candidate >= i {
					cut = candidate
					break
				}
			}
			plan.CutIndex = cut
			markSplitTurn(&plan, entries, start, cut)
			return plan
		}
	}
	// 3.预算未用完时切在最早合法切点
	plan.CutIndex = cuts[0]
	markSplitTurn(&plan, entries, start, cuts[0])
	return plan
}

func markSplitTurn(plan *CutPlan, entries []ContextEntry, start, cut int) {
	if cut >= len(entries) {
		return
	}
	if entryRole(entries[cut]) != llm.RoleAssistant {
		return
	}
	for i := cut - 1; i >= start; i-- {
		if entryRole(entries[i]) == llm.RoleUser {
			plan.TurnStartIndex = i
			plan.IsSplitTurn = true
			return
		}
	}
}

func entryTokens(entry ContextEntry) int {
	total := 0
	for _, message := range entry.Messages {
		total += MessageTokens(message)
	}
	return total
}

// entryRole 返回 entry 中首个对话消息的角色，system/tool-only entry 不构成切点
func entryRole(entry ContextEntry) llm.Role {
	for _, message := range entry.Messages {
		switch message.Role() {
		case llm.RoleUser, llm.RoleAssistant:
			return message.Role()
		}
	}
	return ""
}

func isCutBoundary(entry ContextEntry) bool {
	if entry.Kind == ContextCompaction {
		return false
	}
	role := entryRole(entry)
	return role == llm.RoleUser || role == llm.RoleAssistant
}
