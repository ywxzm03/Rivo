package session

import (
	"encoding/json"
	"fmt"

	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// BuildContext 从当前叶子沿父链回溯构建模型上下文视图：
// 1.取活跃路径；2.应用最近的 compaction 替换历史；3.应用 context_edit 投影；4.生成带来源的上下文条目
func (m *SessionManager) BuildContext() ctxview.ContextView {
	path := m.activePath()
	ctx := ctxview.ContextView{ThinkingLevel: "off"}

	// 1.设置提取走完整路径：model 优先取最后一个 model_change，否则兜底最后一条 assistant 消息
	applySettings(path, &ctx)

	// 2.取路径上最后一个 compaction，用它替换 firstKeptEntryId 之前的历史
	compaction, selected := selectCompaction(path)

	// 3.每个目标取路径上最新一条 context_edit
	edits := collectContextEdits(selected)
	if len(edits) > 0 {
		ctx.ContentReplacements = make(map[string][]llm.Content, len(edits))
		for targetID, edit := range edits {
			if edit.Replacement == nil {
				ctx.ContentReplacements[targetID] = nil
				continue
			}
			content := make([]llm.Content, len(edit.Replacement.Content))
			copy(content, edit.Replacement.Content)
			ctx.ContentReplacements[targetID] = content
		}
	}

	// 4.生成上下文条目：compaction 与被省略的目标有特殊处理，其余类型不产出条目
	ctx.Entries, ctx.UsageStale = buildEntries(path, selected, compaction, edits)
	return ctx
}

// BranchToolDetails 按原始父链读取成功工具详情，不受模型上下文裁剪影响
func (m *SessionManager) BranchToolDetails(toolName string) ([]json.RawMessage, error) {
	var details []json.RawMessage
	for _, entry := range m.activePath() {
		message, ok := entry.(*MessageEntry)
		if !ok {
			continue
		}
		result, ok := message.Message.(llm.ToolResultMessage)
		if !ok || result.ToolName != toolName || result.IsError || result.Details == nil {
			continue
		}
		raw, err := json.Marshal(result.Details)
		if err != nil {
			return nil, fmt.Errorf("session: encode tool details for entry %s: %w", entry.base().ID, err)
		}
		details = append(details, raw)
	}
	return details, nil
}

// applySettings 从完整路径提取思考等级与模型
func applySettings(path []Entry, ctx *ctxview.ContextView) {
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
}

// selectCompaction 返回最后一个 compaction 与其之后要参与投影的条目
func selectCompaction(path []Entry) (*CompactionEntry, []Entry) {
	var compaction *CompactionEntry
	compactionIdx := -1
	for i, e := range path {
		if c, ok := e.(*CompactionEntry); ok {
			compaction, compactionIdx = c, i
		}
	}
	if compaction == nil {
		return nil, path
	}
	selected := []Entry{compaction}
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
	return compaction, append(selected, path[compactionIdx+1:]...)
}

// collectContextEdits 汇总每个目标最新的一条 context_edit
func collectContextEdits(selected []Entry) map[string]*ContextEditEntry {
	edits := make(map[string]*ContextEditEntry)
	for _, e := range selected {
		if ce, ok := e.(*ContextEditEntry); ok {
			edits[ce.TargetID] = ce
		}
	}
	return edits
}

// buildEntries 生成投影条目，并判断最后一条 assistant 的 usage 是否已过期
func buildEntries(path, selected []Entry, compaction *CompactionEntry, edits map[string]*ContextEditEntry) ([]ctxview.ContextEntry, bool) {
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
	var entries []ctxview.ContextEntry
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
			entries = append(entries, ctxview.ContextEntry{
				SourceID: e.base().ID,
				Kind:     ctxview.ContextMessage,
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
				Content: []llm.Content{llm.TextContent{Text: ctxview.WrappedSummary(e.Summary)}},
			})
			entries = append(entries, ctxview.ContextEntry{
				SourceID: e.base().ID,
				Kind:     ctxview.ContextCompaction,
				Messages: messages,
				Summary:  e.Summary,
				Details:  cloneFileLists(e.Details),
			})
		}
	}
	return entries, lastAssistantStale
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
