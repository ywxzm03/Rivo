package compaction

import "github.com/ywxzm03/Rivo/internal/llm"

// ContextKind 标记一条模型上下文条目的来源类型
type ContextKind uint8

const (
	ContextMessage ContextKind = iota
	ContextCompaction
)

// ContextEntry 保存一条上下文条目及其来源 entry；compaction 条目可展开为多条消息
type ContextEntry struct {
	SourceID string
	Kind     ContextKind
	Messages []llm.Message
	Summary  string
}

// ContextView 是当前会话叶子的模型可见视图，也是压缩的唯一输入
type ContextView struct {
	Entries          []ContextEntry
	Provider         string
	Model            string
	ThinkingLevel    string
	HasThinkingLevel bool
	UsageStale       bool
}

// Messages 按投影顺序展开模型上下文消息
func (v ContextView) Messages() []llm.Message {
	var messages []llm.Message
	for _, entry := range v.Entries {
		messages = append(messages, entry.Messages...)
	}
	return messages
}

// MessageCount 统计条目展开后的模型消息数量
func MessageCount(entries []ContextEntry) int {
	total := 0
	for _, entry := range entries {
		total += len(entry.Messages)
	}
	return total
}

// SummaryMessages 提取可供摘要模型阅读的消息，排除 compaction 与 system 消息
func SummaryMessages(entries []ContextEntry) []llm.Message {
	var messages []llm.Message
	for _, entry := range entries {
		if entry.Kind == ContextCompaction {
			continue
		}
		for _, message := range entry.Messages {
			if message.Role() == llm.RoleSystem {
				continue
			}
			messages = append(messages, message)
		}
	}
	return messages
}
