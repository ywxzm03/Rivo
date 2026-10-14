package session

import (
	compact "github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// SessionContext 是供 TUI 快照投影使用的扁平会话上下文；压缩与 Agent 运行使用 BuildContext 返回的带来源视图
type SessionContext struct {
	Messages         []llm.Message
	EntryIDs         []string
	Provider         string
	Model            string
	ThinkingLevel    string
	LastSummary      string
	HasThinkingLevel bool
	UsageStale       bool
}

// BuildSessionContext 将带来源上下文视图展开为旧版快照投影所需的一一对应消息与 entry ID
func (m *SessionManager) BuildSessionContext() SessionContext {
	view := m.BuildContext()
	ctx := SessionContext{
		Provider:         view.Provider,
		Model:            view.Model,
		ThinkingLevel:    view.ThinkingLevel,
		HasThinkingLevel: view.HasThinkingLevel,
		UsageStale:       view.UsageStale,
	}
	for _, entry := range view.Entries {
		ctx.Messages = append(ctx.Messages, entry.Messages...)
		for range entry.Messages {
			ctx.EntryIDs = append(ctx.EntryIDs, entry.SourceID)
		}
		if entry.Kind == compact.ContextCompaction {
			ctx.LastSummary = entry.Summary
		}
	}
	return ctx
}

// BuildContext 从当前叶子沿父链回溯构建模型上下文视图：
// 1.取活跃路径；2.应用最近的 compaction 替换历史；3.应用 context_edit 投影；4.生成带来源的上下文条目
func (m *SessionManager) BuildContext() compact.ContextView {
	path := m.activePath()
	ctx := compact.ContextView{ThinkingLevel: "off"}

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
		kept := false
		for _, e := range path[:compactionIdx] {
			if e.base().ID == compaction.FirstKeptEntryID {
				kept = true
			}
			if kept {
				// compaction 快照已替代边界前的 system entry，避免旧 prompt 重复进入模型上下文
				if message, ok := e.(*MessageEntry); ok {
					if _, isSystem := message.Message.(llm.SystemMessage); isSystem {
						continue
					}
				}
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

	// 4.生成上下文条目：compaction 与被省略的目标有特殊处理，其余类型不产出条目
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
			var message llm.Message
			if edit != nil {
				if edit.Replacement == nil {
					continue
				}
				message = withReplacedContent(e.Message, edit.Replacement.Content)
			} else {
				message = e.Message
			}
			ctx.Entries = append(ctx.Entries, compact.ContextEntry{
				SourceID: e.base().ID,
				Kind:     compact.ContextMessage,
				Messages: []llm.Message{message},
			})
			if _, ok := message.(llm.AssistantMessage); ok {
				lastAssistantStale = compaction != nil && pathIdx[e.base().ID] < compactionIdx
			}
		case *CompactionEntry:
			// 保留区间内可能残留更早的 compaction，只有最新的这个产出快照与摘要
			if compaction != nil && e != compaction {
				continue
			}
			// 压缩快照位于摘要之前：它把压缩边界处的 system prompt 合并为一条，
			// 保留区内的 system 条目由此被取代
			messages := make([]llm.Message, 0, 2)
			if e.SystemMessage != nil {
				messages = append(messages, *e.SystemMessage)
			}
			messages = append(messages, llm.UserMessage{
				Content: []llm.Content{llm.TextContent{Text: compact.WrappedSummary(e.Summary)}},
			})
			ctx.Entries = append(ctx.Entries, compact.ContextEntry{
				SourceID: e.base().ID,
				Kind:     compact.ContextCompaction,
				Messages: messages,
				Summary:  e.Summary,
			})
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
