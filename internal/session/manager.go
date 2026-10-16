package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// SessionManager 管理一个树形 JSONL 会话：entry 按文件序追加，leaf 指向当前分支位置，
// 设置先写入隐藏草稿，命名或首条对话消息发布 JSONL；持久化成功后才推进内存
type SessionManager struct {
	header      Header
	entries     []Entry
	byID        map[string]Entry
	leafID      string
	flushed     bool
	dir         string
	sessionFile string
	pending     []Entry
	// repairOffset 在追加回退失败时保留原长度，下次提交先修复再补写
	repairOffset *int64
	// needsNewline 表示会话文件末尾缺少换行（加载残行文件后为 true），追加前先补写
	needsNewline bool
}

// DefaultDir 返回 cwd 对应的默认会话目录 ~/.rivo/agent/sessions/--<编码后的cwd>--/
func DefaultDir(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: failed to get user home directory: %w", err)
	}
	return filepath.Join(home, ".rivo", "agent", "sessions", dirName(cwd)), nil
}

// dirName 把 cwd 编码为安全的目录名：去掉首个路径分隔符，/、\、: 替换为 -，以 -- 包裹
func dirName(cwd string) string {
	trimmed := cwd
	if len(trimmed) > 0 && (trimmed[0] == '/' || trimmed[0] == '\\') {
		trimmed = trimmed[1:]
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-")
	return "--" + replacer.Replace(trimmed) + "--"
}

// New 在 dir 下创建新会话；header 只在内存，正式发布延迟到命名或首条对话消息
func New(cwd, dir string) *SessionManager {
	header := Header{
		Type:      TypeSession,
		Version:   CurrentVersion,
		ID:        newSessionID(),
		Timestamp: nowRFC3339(),
		Cwd:       cwd,
	}
	fileTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(nowRFC3339())
	return &SessionManager{
		header:      header,
		byID:        make(map[string]Entry),
		dir:         dir,
		sessionFile: filepath.Join(dir, fileTimestamp+"_"+header.ID+".jsonl"),
	}
}

// Open 加载指定会话文件；加载成功即视为已完成首次 flush，后续追加直接落盘。
// 0 字节文件按新会话处理并保留显式路径；文件末尾缺换行时补写一个，防止追加与残行粘连
func Open(path, cwd string) (*SessionManager, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session: failed to read session file: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		m := New(cwd, filepath.Dir(path))
		m.sessionFile = path
		// 空文件不含任何内容，移除后再发布，避免残留空文件阻塞首次提交
		_ = os.Remove(path)
		return m, nil
	}
	header, entries, err := parseFile(data)
	if err != nil {
		return nil, err
	}
	m := &SessionManager{
		header:       *header,
		byID:         make(map[string]Entry, len(entries)),
		dir:          filepath.Dir(path),
		sessionFile:  path,
		flushed:      true,
		needsNewline: !bytes.HasSuffix(data, []byte("\n")),
	}
	// 1.按文件序建索引：重复 ID 后者覆盖前者，leaf 取最后一个 entry
	for _, e := range entries {
		m.entries = append(m.entries, e)
		m.byID[e.base().ID] = e
		m.leafID = e.base().ID
	}
	return m, nil
}

// LastEntryID 返回最近追加的 entry ID；还没有任何 entry 时返回空串
func (m *SessionManager) LastEntryID() string {
	if len(m.entries) == 0 {
		return ""
	}
	return m.entries[len(m.entries)-1].base().ID
}

// AppendMessage 追加一条消息，以当前 leaf 为父节点并推进 leaf
func (m *SessionManager) AppendMessage(msg llm.Message) error {
	return m.appendEntry(&MessageEntry{Message: msg})
}

// AppendModelChange 记录中途切换模型
func (m *SessionManager) AppendModelChange(provider, modelID string) error {
	return m.appendEntry(&ModelChangeEntry{Provider: provider, ModelID: modelID})
}

// AppendThinkingLevelChange 记录思考级别变化
func (m *SessionManager) AppendThinkingLevelChange(level string) error {
	return m.appendEntry(&ThinkingLevelChangeEntry{ThinkingLevel: level})
}

// AppendSessionInfo 规范化名称并追加元数据，空名称不提交
func (m *SessionManager) AppendSessionInfo(name string) error {
	name = strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(name))
	if name == "" {
		return errors.New("Usage: /rename <new Name>")
	}
	return m.appendEntry(&SessionInfoEntry{Name: name})
}

// Name 从文件追加顺序取最新名称，切换分支不改变 Thread 名称
func (m *SessionManager) Name() string {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if info, ok := m.entries[i].(*SessionInfoEntry); ok {
			return info.Name
		}
	}
	return ""
}

// AppendUsage 记录不依附消息的模型用量，note 为空时不写入
func (m *SessionManager) AppendUsage(kind, provider, model string, usage llm.Usage, note string) error {
	return m.appendEntry(&UsageEntry{
		Kind:     kind,
		Provider: provider,
		Model:    model,
		Usage:    usageWire(usage),
		Note:     note,
	})
}

// AppendCompaction 追加压缩点；firstKeptEntryID 为空时存自身 ID 表示零保留
func (m *SessionManager) AppendCompaction(summary, firstKeptEntryID string, tokensBefore int, systemMessage *llm.SystemMessage, usage *llm.Usage) error {
	e := &CompactionEntry{
		Summary:          summary,
		FirstKeptEntryID: firstKeptEntryID,
		TokensBefore:     tokensBefore,
		SystemMessage:    systemMessage,
	}
	if usage != nil {
		wire := usageWire(*usage)
		e.Usage = &wire
	}
	if firstKeptEntryID == "" {
		e.ID = m.newEntryID()
		e.FirstKeptEntryID = e.ID
	}
	return m.appendEntry(e)
}

// AppendContextEdit 追加对历史 entry 的分支内改写；目标必须存在、在活跃分支上且为可编辑消息
func (m *SessionManager) AppendContextEdit(targetID string, replacement *Replacement) error {
	// 1.目标存在且在活跃分支上
	target, ok := m.byID[targetID]
	if !ok {
		return fmt.Errorf("session: entry %s not found", targetID)
	}
	if !m.onActiveBranch(targetID) {
		return fmt.Errorf("session: entry %s is not on the active branch", targetID)
	}
	// 2.只有 user/assistant/toolResult 消息可编辑
	me, ok := target.(*MessageEntry)
	if !ok {
		return fmt.Errorf("session: entry %s is not an editable message entry", targetID)
	}
	switch messageRole(me.Message) {
	case "user", "assistant", "toolResult":
	default:
		return fmt.Errorf("session: entry %s is not an editable message entry", targetID)
	}
	return m.appendEntry(&ContextEditEntry{TargetID: targetID, Replacement: replacement})
}

// Branch 只移动内存 leaf 到指定 entry，下一次追加形成新分支，不改写文件
func (m *SessionManager) Branch(entryID string) error {
	if m.HasPending() {
		return &CommitError{Err: errors.New("session: retry pending submission before branching")}
	}
	if _, ok := m.byID[entryID]; !ok {
		return fmt.Errorf("session: entry %s not found", entryID)
	}
	m.leafID = entryID
	return nil
}

// CommitError 表示待提交记录未落盘，调用者必须停止模型与工具的后续推进
type CommitError struct{ Err error }

func (e *CommitError) Error() string { return e.Err.Error() }
func (e *CommitError) Unwrap() error { return e.Err }

// Path 返回会话路径；未命名且无对话时正式文件尚不存在
func (m *SessionManager) Path() string { return m.sessionFile }

// Delete 删除当前会话的正式文件与未发布草稿
func (m *SessionManager) Delete() error {
	var errs []error
	for _, path := range []string{m.sessionFile, m.sessionFile + ".draft"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("session: failed to delete %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// ID 返回会话命名空间
func (m *SessionManager) ID() string { return m.header.ID }

// HasPending 表示存在尚未成功落盘的候选记录
func (m *SessionManager) HasPending() bool { return len(m.pending) > 0 }

// PendingMessages 返回待写批次的消息副本，宿主据此续接提交之后的工作
func (m *SessionManager) PendingMessages() []llm.Message {
	var messages []llm.Message
	for _, e := range m.pending {
		if message, ok := e.(*MessageEntry); ok {
			messages = append(messages, message.Message)
		}
	}
	return messages
}

// PendingContextEdit 返回待写批次中的上下文编辑；没有则返回 nil
func (m *SessionManager) PendingContextEdit() *ContextEditEntry {
	for _, e := range m.pending {
		if edit, ok := e.(*ContextEditEntry); ok {
			return edit
		}
	}
	return nil
}

// AppendMessages 整体提交一个消息批次，返回与消息顺序对应的稳定 entry ID
func (m *SessionManager) AppendMessages(messages []llm.Message) ([]string, error) {
	entries := make([]Entry, len(messages))
	for i, message := range messages {
		entries[i] = &MessageEntry{Message: message}
	}
	err := m.appendEntries(entries)
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.base().ID
	}
	return ids, err
}

// appendEntry 准备候选记录，写入失败时保留它供显式重试
func (m *SessionManager) appendEntry(e Entry) error { return m.appendEntries([]Entry{e}) }

func (m *SessionManager) appendEntries(entries []Entry) error {
	if m.HasPending() {
		return &CommitError{Err: errors.New("session: retry pending submission before appending")}
	}
	parent := m.leafID
	for _, e := range entries {
		base := e.base()
		if base.Type == "" {
			base.Type = entryTypeName(e)
		}
		if base.ID == "" {
			base.ID = m.newEntryID()
		}
		if parent != "" {
			id := parent
			base.ParentID = &id
		}
		base.Timestamp = nowRFC3339()
		parent = base.ID
	}
	m.pending = entries
	return m.RetryPending()
}

// RetryPending 原样补写候选记录；成功前 entries、索引与 leaf 都保持原值
func (m *SessionManager) RetryPending() error {
	if !m.HasPending() {
		return nil
	}
	if m.repairOffset != nil {
		if err := os.Truncate(m.sessionFile, *m.repairOffset); err != nil {
			return &CommitError{Err: err}
		}
		m.repairOffset = nil
	}
	if err := m.persist(m.pending); err != nil {
		return &CommitError{Err: err}
	}
	for _, e := range m.pending {
		m.entries = append(m.entries, e)
		m.byID[e.base().ID] = e
		m.leafID = e.base().ID
	}
	m.pending = nil
	return nil
}

// persist 先编码整个批次，首次发布用完整临时文件，后续追加在失败时回退长度
func (m *SessionManager) persist(entries []Entry) error {
	if !m.flushed {
		all := append(append([]Entry(nil), m.entries...), entries...)
		var data bytes.Buffer
		enc := json.NewEncoder(&data)
		if err := enc.Encode(m.header); err != nil {
			return err
		}
		for _, e := range all {
			if err := enc.Encode(e); err != nil {
				return fmt.Errorf("session: failed to encode entry: %w", err)
			}
		}
		if err := os.MkdirAll(m.dir, 0o755); err != nil {
			return fmt.Errorf("session: failed to create session directory: %w", err)
		}
		f, err := os.CreateTemp(m.dir, ".rivo-session-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err := f.Write(data.Bytes()); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		draft := m.sessionFile + ".draft"
		if !shouldPublish(all) {
			return os.Rename(f.Name(), draft)
		}
		// 同目录重命名发布完整文件，不依赖文件系统支持硬链接
		if _, err := os.Lstat(m.sessionFile); err == nil {
			return fmt.Errorf("session: failed to publish session file: %w", os.ErrExist)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("session: failed to inspect session path: %w", err)
		}
		if err := os.Rename(f.Name(), m.sessionFile); err != nil {
			return fmt.Errorf("session: failed to publish session file: %w", err)
		}
		_ = os.Remove(draft)
		m.flushed = true
		return nil
	}
	var data bytes.Buffer
	if m.needsNewline {
		data.WriteByte('\n')
	}
	enc := json.NewEncoder(&data)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("session: failed to encode entry: %w", err)
		}
	}
	f, err := os.OpenFile(m.sessionFile, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("session: failed to open session file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	n, writeErr := f.Write(data.Bytes())
	if writeErr == nil && n != data.Len() {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		rollbackErr := f.Truncate(info.Size())
		if rollbackErr != nil {
			offset := info.Size()
			m.repairOffset = &offset
		}
		_ = f.Close()
		return fmt.Errorf("session: failed to append batch: %w", errors.Join(writeErr, rollbackErr))
	}
	if err := f.Close(); err != nil {
		// Close 失败也不能发布内存，回退文件长度后允许同 ID 重试
		rollbackErr := os.Truncate(m.sessionFile, info.Size())
		if rollbackErr != nil {
			offset := info.Size()
			m.repairOffset = &offset
		}
		return errors.Join(err, rollbackErr)
	}
	m.needsNewline = false
	return nil
}

// entryTypeName 返回 entry 对应的类型常量
func entryTypeName(e Entry) string {
	switch e.(type) {
	case *MessageEntry:
		return TypeMessage
	case *ModelChangeEntry:
		return TypeModelChange
	case *ThinkingLevelChangeEntry:
		return TypeThinkingLevelChange
	case *UsageEntry:
		return TypeUsage
	case *CompactionEntry:
		return TypeCompaction
	case *ContextEditEntry:
		return TypeContextEdit
	case *SessionInfoEntry:
		return TypeSessionInfo
	default:
		return ""
	}
}

// shouldPublish 命名或产生首条对话后发布正式会话，其余设置留在草稿
func shouldPublish(entries []Entry) bool {
	for _, e := range entries {
		if info, ok := e.(*SessionInfoEntry); ok && info.Name != "" {
			return true
		}
		me, ok := e.(*MessageEntry)
		if !ok {
			continue
		}
		switch messageRole(me.Message) {
		case "user", "assistant":
			return true
		}
	}
	return false
}

// onActiveBranch 判断 entry 是否在当前 leaf 的路径上
func (m *SessionManager) onActiveBranch(entryID string) bool {
	current := m.leafID
	for current != "" {
		if current == entryID {
			return true
		}
		e, ok := m.byID[current]
		if !ok {
			return false
		}
		parent := e.base().ParentID
		if parent == nil {
			return false
		}
		current = *parent
	}
	return false
}

// newEntryID 生成不与现有索引冲突的 entry ID
func (m *SessionManager) newEntryID() string {
	for {
		id := randomHexID()
		if _, taken := m.byID[id]; !taken {
			return id
		}
	}
}
