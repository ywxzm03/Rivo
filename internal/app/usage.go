package app

import (
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// ContextUsage 是当前 active projection 的窗口用量；Tokens 在 provider usage 缺失或陈旧时为估算值。
type ContextUsage struct {
	Tokens        int
	ContextWindow int
	Available     bool
}

// UsageTotals 返回当前会话全部持久化 usage，供非 TUI 查询和后续统计展示使用。
func (a *App) UsageTotals() session.UsageTotals {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.session.UsageTotals()
}

// ContextUsage 返回当前已提交上下文的只读 token 快照，不启动 operation 或修改 session。
func (a *App) ContextUsage() ContextUsage {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	model := a.agent.AgentConfig.Model
	if model.ContextWindow <= 0 {
		return ContextUsage{}
	}

	view := a.session.BuildContext()
	messages := view.Messages()
	if view.UsageStale || !usageAnchorMatchesModel(messages, model) {
		return ContextUsage{Tokens: compaction.EstimateContextTokens(messages), ContextWindow: model.ContextWindow, Available: true}
	}
	return ContextUsage{Tokens: compaction.ContextTokens(messages), ContextWindow: model.ContextWindow, Available: true}
}

func usageAnchorMatchesModel(messages []llm.Message, model llm.Model) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		assistant, ok := messages[i].(llm.AssistantMessage)
		if !ok || assistant.StopReason == llm.StopReasonError || assistant.StopReason == llm.StopReasonAborted || assistant.Usage.ContextTokens() <= 0 {
			continue
		}
		if assistant.Provider == "" || assistant.Model == "" {
			continue
		}
		return assistant.Provider == model.Provider && assistant.Model == model.ID
	}
	return false
}
