package session

import (
	compact "github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
)

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
	latestInvalidatingIdx := -1
	for i, e := range path {
		switch e.(type) {
		case *CompactionEntry, *ContextEditEntry:
			latestInvalidatingIdx = i
		}
	}
	// lastAssistantStale 记录最后一条 assistant 的 usage 是否早于最近一次会改变 projection 的 entry
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
				lastAssistantStale = latestInvalidatingIdx >= 0 && pathIdx[e.base().ID] < latestInvalidatingIdx
			}
		case *CompactionEntry:
			if compaction != nil && e != compaction {
				continue
			}
			// 压缩快照位于摘要之前，保留区内的 system 条目由此被取代
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
				Details:  cloneFileLists(e.Details),
			})
		}
	}
	ctx.UsageStale = lastAssistantStale
	return ctx
}

// activePath 从 leaf 沿 parentId 回溯到根并反转，父链断裂时在断点停止
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
