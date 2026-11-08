package tui

import tea "github.com/charmbracelet/bubbletea"

// InputOrigin 标识一次直接用户输入，排队和重试期间保持不变
type InputOrigin struct {
	Generation uint64 // 主会话 UI 代次
	Sequence   uint64 // 当前代次跨 scope 递增的输入编号
	SessionID  string // 接收输入的主会话 ID 或 TaskID
	Text       string // 展开技能前的历史文本
}

// ComposerDraft 保存可独立恢复的输入文本及真实附件归属
type ComposerDraft struct {
	Text         string
	Pastes       map[int]string
	PasteCounter int
	Images       map[int]ImagePart
	ImageCounter int
}

type ComposerHistoryEntry struct {
	Draft    ComposerDraft
	Replayed bool // 恢复会话种子用于跳过持久区相同文本
}

type InputHistoryMetadata struct {
	Generation      uint64
	SessionID       string
	PersistentCount int // 当前代次固定的持久前缀长度
}

type InputHistoryState struct {
	Metadata InputHistoryMetadata
	ScopeID  string
	Replay   []ComposerHistoryEntry
	ResetAll bool
	Warnings []string
}

type HistoryLookupRequest struct {
	Generation     uint64
	ScopeID        string
	RequestID      uint64
	Offset         int
	EditorRevision uint64
}

type HistoryLookupMsg struct {
	Request     HistoryLookupRequest
	Text        string
	Err         error
	Unavailable bool // 适配器确认索引失效，停用本代次持久区
}

type ScopePrepareRequest struct {
	Generation     uint64
	RequestID      uint64
	ScopeID        string
	EditorRevision uint64
}

type ScopePreparedMsg struct {
	Request ScopePrepareRequest
	Replay  []ComposerHistoryEntry
	Err     error
}

type HistoryRecordMsg struct {
	Origin InputOrigin
	Err    error
}

// HistoryHandlers 仅在 Cmd 内读取磁盘，清空草稿的同步入口只负责入队
type HistoryHandlers struct {
	Lookup        func(HistoryLookupRequest) tea.Cmd
	PrepareScope  func(ScopePrepareRequest) tea.Cmd
	RecordCleared func(InputOrigin) tea.Cmd
	Acknowledge   func(InputOrigin)
}
