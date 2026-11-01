package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// SessionManager 管理一个树形 JSONL 会话：entry 按文件序追加，leaf 指向当前分支位置；
// 设置先留在内存，首条对话消息发布 JSONL，持久化成功后才推进内存
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
	// needsNewline 表示会话文件末尾缺少换行，追加前先补写
	needsNewline bool
	// fileHistoryRecords 保留 JSONL 中不参与树结构的文件历史记录
	fileHistoryRecords []filehistory.Record
	fileHistory        *filehistory.Store
	warnings           []string
	persistMu          sync.Mutex
}

const maxSessionWarnings = 100

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

// New 在 dir 下创建新会话，header 只在内存，正式发布延迟到首条对话消息
func New(cwd, dir string) *SessionManager {
	return newWithID(cwd, dir, newSessionID())
}

// NewWithID 创建指定 ID 且延迟发布的会话
func NewWithID(cwd, dir, id string) (*SessionManager, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	return newWithID(cwd, dir, id), nil
}

func newWithID(cwd, dir, id string) *SessionManager {
	header := Header{
		Type:      TypeSession,
		Version:   CurrentVersion,
		ID:        id,
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

// NewAt 创建调用方指定路径与 ID 的会话。子任务用它把任务号同时作为会话号,
// 不再额外生成一份随机标识
func NewAt(cwd, path, id string) (*SessionManager, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	m := newWithID(cwd, filepath.Dir(path), id)
	m.sessionFile = path
	return m, nil
}

// Open 加载指定会话文件，加载成功即视为已完成首次 flush；
// 0 字节文件按新会话处理，文件末尾缺换行时补写一个，防止追加与残行粘连
func Open(path, cwd string) (*SessionManager, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session: failed to read session file: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		m := New(cwd, filepath.Dir(path))
		m.sessionFile = path
		_ = os.Remove(path)
		return m, nil
	}
	return openData(path, data)
}

// OpenWithID 打开必须已存在且身份匹配的会话。
// 空白文件、缺失会话头与 ID 不匹配都拒绝,不能退回新建或删除文件
func OpenWithID(path, expectedID string) (*SessionManager, error) {
	if err := ValidateID(expectedID); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session: failed to read session file: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("session: %s is empty and cannot be reopened as %s", path, expectedID)
	}
	m, err := openData(path, data)
	if err != nil {
		return nil, err
	}
	if m.header.ID != expectedID {
		return nil, fmt.Errorf("session: %s belongs to %s, want %s", path, m.header.ID, expectedID)
	}
	return m, nil
}

// openData 从已读入的文件内容重建会话,调用方负责空文件与身份校验
func openData(path string, data []byte) (*SessionManager, error) {
	header, entries, records, warnings, err := parseFileWithRecords(data)
	if err != nil {
		return nil, err
	}
	m := &SessionManager{
		header:             *header,
		byID:               make(map[string]Entry, len(entries)),
		dir:                filepath.Dir(path),
		sessionFile:        path,
		flushed:            true,
		needsNewline:       !bytes.HasSuffix(data, []byte("\n")),
		fileHistoryRecords: records,
		warnings:           boundedWarnings(warnings),
	}
	// 1.按文件序建索引：重复 ID 后者覆盖前者，leaf 取最后一个 entry
	for _, e := range entries {
		m.entries = append(m.entries, e)
		m.byID[e.base().ID] = e
		m.leafID = e.base().ID
	}
	return m, nil
}

func (m *SessionManager) LastEntryID() string {
	if len(m.entries) == 0 {
		return ""
	}
	return m.entries[len(m.entries)-1].base().ID
}

func (m *SessionManager) LeafEntryID() string { return m.leafID }

func (m *SessionManager) AppendMessage(msg llm.Message) error {
	return m.appendEntry(&MessageEntry{Message: priceMessage(msg)})
}

func (m *SessionManager) AppendModelChange(provider, modelID string) error {
	return m.appendEntry(&ModelChangeEntry{Provider: provider, ModelID: modelID})
}

func (m *SessionManager) AppendThinkingLevelChange(level string) error {
	return m.appendEntry(&ThinkingLevelChangeEntry{ThinkingLevel: level})
}

// AppendSessionInfo 规范化名称并追加元数据，空名称不提交
func (m *SessionManager) AppendSessionInfo(name string) error {
	name = strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(name))
	if name == "" {
		return errors.New("usage: /rename <new Name>")
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

func (m *SessionManager) AppendUsage(kind, provider, model string, usage llm.Usage, note string) error {
	if usage.Cost == nil {
		usage.Cost = llm.PriceUsage(llm.Model{Provider: provider, ID: model}, usage)
	}
	return m.appendEntry(&UsageEntry{
		Kind:     kind,
		Provider: provider,
		Model:    model,
		Usage:    cloneUsage(usage),
		Note:     note,
	})
}

// AppendCompaction 追加不带文件列表的压缩点，firstKeptEntryID 为空时存自身 ID 表示零保留
func (m *SessionManager) AppendCompaction(summary, firstKeptEntryID string, tokensBefore int, systemMessage *llm.SystemMessage, usage *llm.Usage) error {
	return m.AppendCompactionWithDetails(summary, firstKeptEntryID, tokensBefore, systemMessage, usage, nil)
}

func (m *SessionManager) AppendCompactionWithDetails(summary, firstKeptEntryID string, tokensBefore int, systemMessage *llm.SystemMessage, usage *llm.Usage, details *ctxview.FileLists) error {
	e := &CompactionEntry{
		Summary:          summary,
		FirstKeptEntryID: firstKeptEntryID,
		TokensBefore:     tokensBefore,
		Details:          cloneFileLists(details),
		SystemMessage:    systemMessage,
	}
	if usage != nil {
		cloned := cloneUsage(*usage)
		e.Usage = &cloned
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

// Branch 只移动内存 leaf 到指定 entry，下一次追加形成新分支
func (m *SessionManager) Branch(entryID string, keepIDs ...string) error {
	if m.HasPending() {
		return &CommitError{Err: errors.New("session: retry pending submission before branching")}
	}
	if _, ok := m.byID[entryID]; !ok {
		return fmt.Errorf("session: entry %s not found", entryID)
	}
	m.leafID = entryID
	m.PruneFileHistoryToActiveBranch(keepIDs...)
	return nil
}

// CommitError 表示待提交记录未落盘，调用者必须停止后续推进
type CommitError struct{ Err error }

func (e *CommitError) Error() string { return e.Err.Error() }
func (e *CommitError) Unwrap() error { return e.Err }

// Path 返回会话路径，正式文件在首次发布前尚不存在
func (m *SessionManager) Path() string { return m.sessionFile }

func (m *SessionManager) Delete() error {
	var errs []error
	for _, path := range []string{m.sessionFile, m.sessionFile + ".draft"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("session: failed to delete %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func (m *SessionManager) ID() string { return m.header.ID }

// Cwd 返回会话 header 记录的工作目录
func (m *SessionManager) Cwd() string { return m.header.Cwd }

func (m *SessionManager) Warnings() []string {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	return append([]string(nil), m.warnings...)
}

func (m *SessionManager) AddWarning(warning string) {
	if warning == "" {
		return
	}
	m.persistMu.Lock()
	if slices.Contains(m.warnings, warning) {
		m.persistMu.Unlock()
		return
	}
	if len(m.warnings) >= maxSessionWarnings {
		copy(m.warnings, m.warnings[len(m.warnings)-maxSessionWarnings+1:])
		m.warnings = m.warnings[:maxSessionWarnings-1]
	}
	m.warnings = append(m.warnings, warning)
	m.persistMu.Unlock()
}

func boundedWarnings(warnings []string) []string {
	if len(warnings) <= maxSessionWarnings {
		return append([]string(nil), warnings...)
	}
	return append([]string(nil), warnings[len(warnings)-maxSessionWarnings:]...)
}

// AttachFileHistory 把当前 session 的 JSONL metadata 与备份目录连接起来
func (m *SessionManager) AttachFileHistory(root string) {
	// 恢复会话时只让当前活跃路径参与新快照，避免旧分支的文件集合污染当前 turn
	m.fileHistory = filehistory.New(root, m.ID(), m.fileHistoryRecords, m.activeUserIDs(), m.appendFileHistoryRecord)
}

func (m *SessionManager) FileHistory() *filehistory.Store { return m.fileHistory }

func (m *SessionManager) MakeFileHistorySnapshot(messageID string) error {
	if m.fileHistory == nil {
		return nil
	}
	return m.fileHistory.MakeSnapshot(messageID)
}

func (m *SessionManager) PruneFileHistoryToActiveBranch(keepIDs ...string) {
	if m.fileHistory != nil {
		m.fileHistory.PruneActiveBranch(m.activeUserIDs(keepIDs...))
	}
}

func (m *SessionManager) activeUserIDs(keepIDs ...string) map[string]bool {
	ids := make(map[string]bool)
	for _, entry := range m.activePath() {
		if message, ok := entry.(*MessageEntry); ok && messageRole(message.Message) == "user" {
			ids[entry.base().ID] = true
		}
	}
	for _, id := range keepIDs {
		if id != "" {
			ids[id] = true
		}
	}
	return ids
}

func (m *SessionManager) HasPending() bool { return len(m.pending) > 0 }

// PendingMessages 返回待写批次的消息副本
func (m *SessionManager) PendingMessages() []llm.Message {
	var messages []llm.Message
	for _, e := range m.pending {
		if message, ok := e.(*MessageEntry); ok {
			messages = append(messages, message.Message)
		}
	}
	return messages
}

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
		message = priceMessage(message)
		entries[i] = &MessageEntry{Message: message}
	}
	err := m.appendEntries(entries)
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.base().ID
	}
	return ids, err
}

func priceMessage(message llm.Message) llm.Message {
	switch message := message.(type) {
	case llm.AssistantMessage:
		if message.Usage.Cost == nil {
			message.Usage.Cost = llm.PriceUsage(llm.Model{Provider: message.Provider, ID: message.Model}, message.Usage)
		}
		return message
	case *llm.AssistantMessage:
		if message == nil {
			return nil
		}
		copy := *message
		if copy.Usage.Cost == nil {
			copy.Usage.Cost = llm.PriceUsage(llm.Model{Provider: copy.Provider, ID: copy.Model}, copy.Usage)
		}
		return copy
	default:
		return message
	}
}

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

// persist 先编码整个批次，首次发布创建正式文件，后续追加在失败时回退长度
func (m *SessionManager) persist(entries []Entry) error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
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
		if !shouldPublish(all) {
			return nil
		}
		if err := os.MkdirAll(m.dir, 0o755); err != nil {
			return fmt.Errorf("session: failed to create session directory: %w", err)
		}
		f, err := os.OpenFile(m.sessionFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("session: failed to create session file: %w", err)
		}
		removeOnFailure := true
		defer func() {
			if removeOnFailure {
				_ = f.Close()
				_ = os.Remove(m.sessionFile)
			}
		}()
		n, writeErr := f.Write(data.Bytes())
		if writeErr == nil && n != data.Len() {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			return fmt.Errorf("session: failed to write session file: %w", writeErr)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("session: failed to close session file: %w", err)
		}
		removeOnFailure = false
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

// appendFileHistoryRecord 追加 metadata，不改变 entries、leaf 或树父链
func (m *SessionManager) appendFileHistoryRecord(record filehistory.Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	if !m.flushed {
		return errors.New("session: file history cannot be written before session publish")
	}
	if m.repairOffset != nil {
		if err := os.Truncate(m.sessionFile, *m.repairOffset); err != nil {
			return &CommitError{Err: err}
		}
		m.repairOffset = nil
	}
	if m.needsNewline {
		data = append([]byte{'\n'}, data...)
	}
	f, err := os.OpenFile(m.sessionFile, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		rollbackErr := f.Truncate(info.Size())
		if rollbackErr != nil {
			offset := info.Size()
			m.repairOffset = &offset
		}
		_ = f.Close()
		return errors.Join(writeErr, rollbackErr)
	}
	if err := f.Close(); err != nil {
		rollbackErr := os.Truncate(m.sessionFile, info.Size())
		if rollbackErr != nil {
			offset := info.Size()
			m.repairOffset = &offset
		}
		return errors.Join(err, rollbackErr)
	}
	m.needsNewline = false
	m.fileHistoryRecords = append(m.fileHistoryRecords, record)
	return nil
}

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

// shouldPublish 产生首条对话后发布正式会话
func shouldPublish(entries []Entry) bool {
	for _, e := range entries {
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

func (m *SessionManager) newEntryID() string {
	for {
		id := randomHexID()
		if _, taken := m.byID[id]; !taken {
			return id
		}
	}
}
