package session

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// TreeEntry 是会话树的只读投影，不暴露可变的内部 Entry。
type TreeEntry struct {
	ID       string
	ParentID string
	Type     string
	Role     string
	Text     string
}

// TreeSnapshot 包含完整树与当前内存叶子；调用方据此构建自己的显示投影。
type TreeSnapshot struct {
	LeafID  string
	Entries []TreeEntry
}

// RewindResult 描述一次 user 回退；回退本身只移动内存 leaf。
type RewindResult struct {
	PreviousLeafID string
	UserID         string
	ParentID       string
	Text           string
}

// LeafID 返回当前内存叶子。未提交或空会话返回空串。
func (m *SessionManager) LeafID() string { return m.leafID }

// Tree 返回按 JSONL 追加顺序排列的防御性树快照。
func (m *SessionManager) Tree() TreeSnapshot {
	entries := make([]TreeEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		base := entry.base()
		projected := TreeEntry{ID: base.ID, Type: base.Type}
		if base.ParentID != nil {
			projected.ParentID = *base.ParentID
		}
		if message, ok := entry.(*MessageEntry); ok {
			projected.Role = messageRole(message.Message)
			projected.Text = treeMessageText(message.Message)
		}
		entries = append(entries, projected)
	}
	return TreeSnapshot{LeafID: m.leafID, Entries: entries}
}

// ResetLeaf 把当前 leaf 重置为空树位置；不会写入会话文件。
func (m *SessionManager) ResetLeaf() error {
	if m.HasPending() {
		return &CommitError{Err: errors.New("session: retry pending submission before branching")}
	}
	m.leafID = ""
	return nil
}

// RewindUser 回到目标 user 的 parent，并返回原 user 文本供编辑器恢复。
// 目标可以来自历史分支，但必须是已提交的 user message。
func (m *SessionManager) RewindUser(entryID string) (RewindResult, error) {
	if m.HasPending() {
		return RewindResult{}, &CommitError{Err: errors.New("session: retry pending submission before branching")}
	}
	entry, ok := m.byID[entryID]
	if !ok {
		return RewindResult{}, fmt.Errorf("session: entry %s not found", entryID)
	}
	message, ok := entry.(*MessageEntry)
	if !ok || messageRole(message.Message) != "user" {
		return RewindResult{}, fmt.Errorf("session: entry %s is not a user message", entryID)
	}
	parentID := ""
	if parent := entry.base().ParentID; parent != nil {
		parentID = *parent
		if _, exists := m.byID[parentID]; !exists {
			return RewindResult{}, fmt.Errorf("session: parent %s for entry %s not found", parentID, entryID)
		}
	}
	result := RewindResult{
		PreviousLeafID: m.leafID,
		UserID:         entryID,
		ParentID:       parentID,
		Text:           treeMessageText(message.Message),
	}
	m.leafID = parentID
	return result, nil
}

func treeMessageText(message llm.Message) string {
	var parts []string
	switch message := message.(type) {
	case llm.UserMessage:
		for _, content := range message.Content {
			if text, ok := content.(llm.TextContent); ok {
				parts = append(parts, text.Text)
			}
		}
	case llm.AssistantMessage:
		for _, content := range message.Content {
			if text, ok := content.(llm.TextContent); ok {
				parts = append(parts, text.Text)
			}
		}
	case llm.ToolResultMessage:
		for _, content := range message.Content {
			if text, ok := content.(llm.TextContent); ok {
				parts = append(parts, text.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
