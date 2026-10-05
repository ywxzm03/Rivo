package session

import (
	compact "github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// SessionContext 从当前叶子恢复出的 LLM 上下文与设置
type SessionContext struct {
	// Messages 与 EntryIDs 一一同序：每条 LLM 消息对应其来源 entry（摘要消息对应 compaction entry），
	// 压缩在会话树上定位切点后经 EntryIDs 取 FirstKeptEntryID
	Messages      []llm.Message
	EntryIDs      []string
	Provider      string
	Model         string
	ThinkingLevel string
	// LastSummary 为最近一次压缩的原始摘要文本，供压缩增量合并与会话恢复使用
	LastSummary string
	// HasThinkingLevel 表示活跃路径上存在显式的思考等级记录，用于区分「记录过 off」与「从未记录」
	HasThinkingLevel bool
	// UsageStale 表示最后一条 assistant 消息位于最近一次压缩之前，其 usage 反映压缩前的大上下文，
	// 此时 token 计费应改用纯消息体估算
	UsageStale bool
}

// BuildContext 从当前叶子沿父链回溯构建 LLM 上下文：
// 1.取活跃路径；2.应用最近的 compaction 替换历史；3.应用 context_edit 投影；4.转换为 LLM 消息
func (m *SessionManager) BuildContext() SessionContext {
	path := m.activePath()
	ctx := SessionContext{ThinkingLevel: "off"}

	// 1.设置提取走完整路径：model 优先取最后一个 model_change，否则兜底最后一条 assistant 消息
	var modelChange *ModelChangeEntry
	var lastAssistant llm.AssistantMessage
	for _, e := range path {
		switch e := e.(type) {
		case *ModelChangeEntry:
			modelChange = e
		case *ThinkingLevelChangeEntry:
			ctx.ThinkingLevel = e.ThinkingLevel
			ctx.HasThinkingLevel = true
		case *MessageEntry:
			if a, ok := e.Message.(llm.AssistantMessage); ok {
				lastAssistant = a
			}
		}
	}
	switch {
	case modelChange != nil:
		ctx.Provider, ctx.Model = modelChange.Provider, modelChange.ModelID
	case lastAssistant.Provider != "" || lastAssistant.Model != "":
		ctx.Provider, ctx.Model = lastAssistant.Provider, lastAssistant.Model
	}

	// 2.取路径上最后一个 compaction，用它替换 firstKeptEntryId 之前的历史
	selected := path
	var compaction *CompactionEntry
	compactionIdx := -1
	for i, e := range path {
		if c, ok := e.(*CompactionEntry); ok {
			compaction, compactionIdx = c, i
		}
	}
	if compaction != nil {
		selected = []Entry{compaction}
		ctx.LastSummary = compaction.Summary
		kept := false
		for _, e := range path[:compactionIdx] {
			if e.base().ID == compaction.FirstKeptEntryID {
				kept = true
			}
			if kept {
				selected = append(selected, e)
			}
		}
		selected = append(selected, path[compactionIdx+1:]...)
	}

	// 3.每个目标取路径上最新一条 context_edit
	edits := make(map[string]*ContextEditEntry)
	for _, e := range selected {
		if ce, ok := e.(*ContextEditEntry); ok {
			edits[ce.TargetID] = ce
		}
	}

	// 4.转换消息：compaction 与被省略的目标有特殊处理，其余类型不产出消息
	pathIdx := make(map[string]int, len(path))
	for i, e := range path {
		pathIdx[e.base().ID] = i
	}
	// lastAssistantStale 记录最后一条 assistant 的 entry 是否早于最近一次压缩，
	// 与消息在投影中的先后无关（保留尾部的 assistant 在投影里排在摘要之后）
	lastAssistantStale := false
	for _, e := range selected {
		switch e := e.(type) {
		case *MessageEntry:
			edit := edits[e.base().ID]
			if edit != nil {
				if edit.Replacement == nil {
					continue
				}
				ctx.Messages = append(ctx.Messages, withReplacedContent(e.Message, edit.Replacement.Content))
				ctx.EntryIDs = append(ctx.EntryIDs, e.base().ID)
			} else {
				ctx.Messages = append(ctx.Messages, e.Message)
				ctx.EntryIDs = append(ctx.EntryIDs, e.base().ID)
			}
			if _, ok := ctx.Messages[len(ctx.Messages)-1].(llm.AssistantMessage); ok {
				lastAssistantStale = compaction != nil && pathIdx[e.base().ID] < compactionIdx
			}
		case *CompactionEntry:
			// 保留区间内可能残留更早的 compaction，只有最新的这个产出摘要
			if compaction != nil && e != compaction {
				continue
			}
			ctx.Messages = append(ctx.Messages, llm.UserMessage{
				Content: []llm.Content{llm.TextContent{Text: compact.WrappedSummary(e.Summary)}},
			})
			ctx.EntryIDs = append(ctx.EntryIDs, e.base().ID)
		}
	}
	ctx.UsageStale = lastAssistantStale
	return ctx
}

// activePath 从 leaf 沿 parentId 回溯到根并反转；父链断裂时在断点停止
func (m *SessionManager) activePath() []Entry {
	if m.leafID == "" {
		return nil
	}
	var path []Entry
	current := m.byID[m.leafID]
	for current != nil {
		path = append(path, current)
		parent := current.base().ParentID
		if parent == nil {
			break
		}
		current = m.byID[*parent]
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// withReplacedContent 返回仅替换了 content 的消息副本
func withReplacedContent(msg llm.Message, content []llm.Content) llm.Message {
	switch m := msg.(type) {
	case llm.UserMessage:
		m.Content = content
		return m
	case llm.AssistantMessage:
		m.Content = content
		return m
	case llm.ToolResultMessage:
		m.Content = content
		return m
	default:
		return msg
	}
}
