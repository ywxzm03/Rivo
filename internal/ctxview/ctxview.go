// Package ctxview 定义会话投影的共享值类型。
// 它被 session（生产投影）与 compaction（消费投影）共同引用，
// 因此不能依赖这两个包中的任何一个，只依赖 llm。
package ctxview

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
	// Details 保存压缩点的结构化元数据
	Details *FileLists
}

// ContextView 是当前会话叶子的模型可见视图，也是压缩的唯一输入
type ContextView struct {
	Entries          []ContextEntry
	Provider         string
	Model            string
	ThinkingLevel    string
	HasThinkingLevel bool
	UsageStale       bool
	// ContentReplacements is the active branch's latest replacement state.
	// It lets sidechain resume retain the same projection semantics as the
	// source transcript instead of flattening edits into unrelated messages.
	ContentReplacements map[string][]llm.Content
}

// Messages 按条目顺序展开全部消息
func (v ContextView) Messages() []llm.Message {
	var messages []llm.Message
	for _, entry := range v.Entries {
		messages = append(messages, entry.Messages...)
	}
	return messages
}

// MessageCount 统计条目内的消息总数
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

// FileLists 记录摘要历史中读过的文件与改动过的文件
type FileLists struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// 摘要回插上下文时的包装文本。session 与 compaction 共用，
// 保证落盘摘要与回放摘要的格式完全一致
const (
	compactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	compactionSummarySuffix = "\n</summary>"
)

// WrappedSummary 把摘要包成回放用的固定格式
func WrappedSummary(summary string) string {
	return compactionSummaryPrefix + summary + compactionSummarySuffix
}
