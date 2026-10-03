package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// SessionManager 管理一个树形 JSONL 会话：entry 按文件序追加，leaf 指向当前分支位置，
// 首条 user 或 assistant 消息触发落盘，之后逐行追加
type SessionManager struct {
	header      Header
	entries     []Entry
	byID        map[string]Entry
	leafID      string
	flushed     bool
	dir         string
	sessionFile string
	unwritten   Entry
	// needsNewline 表示会话文件末尾缺少换行（加载残行文件后为 true），追加前先补写
	needsNewline bool
}

// DefaultDir 返回 cwd 对应的默认会话目录 ~/.rivo/agent/sessions/--<编码后的cwd>--/
func DefaultDir(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: 获取用户主目录失败: %w", err)
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

// New 在 dir 下创建新会话；header 只在内存，文件名此时确定，落盘延迟到首条对话消息
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
		return nil, fmt.Errorf("session: 读取会话文件失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		m := New(cwd, filepath.Dir(path))
		m.sessionFile = path
		// 空文件不含任何内容，移除后由 flush 独占创建，避免 O_EXCL 与残留空文件冲突
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
func (m *SessionManager) AppendCompaction(summary, firstKeptEntryID string, tokensBefore int, systemPrompt string, usage *llm.Usage) error {
	e := &CompactionEntry{
		Summary:          summary,
		FirstKeptEntryID: firstKeptEntryID,
		TokensBefore:     tokensBefore,
		SystemPrompt:     systemPrompt,
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
		return fmt.Errorf("session: entry %s 不存在", targetID)
	}
	if !m.onActiveBranch(targetID) {
		return fmt.Errorf("session: entry %s 不在活跃分支上", targetID)
	}
	// 2.只有 user/assistant/toolResult 消息可编辑
	me, ok := target.(*MessageEntry)
	if !ok {
		return fmt.Errorf("session: entry %s 不是可编辑的消息 entry", targetID)
	}
	switch messageRole(me.Message) {
	case "user", "assistant", "toolResult":
	default:
		return fmt.Errorf("session: entry %s 不是可编辑的消息 entry", targetID)
	}
	return m.appendEntry(&ContextEditEntry{TargetID: targetID, Replacement: replacement})
}

// Branch 只移动内存 leaf 到指定 entry，下一次追加形成新分支，不改写文件
func (m *SessionManager) Branch(entryID string) error {
	if _, ok := m.byID[entryID]; !ok {
		return fmt.Errorf("session: entry %s 不存在", entryID)
	}
	m.leafID = entryID
	return nil
}

// appendEntry 填充 entry 的树结构字段，推进内存状态后按 flush 状态落盘
func (m *SessionManager) appendEntry(e Entry) error {
	base := e.base()
	if base.Type == "" {
		base.Type = entryTypeName(e)
	}
	if base.ID == "" {
		base.ID = m.newEntryID()
	}
	if m.leafID != "" {
		parent := m.leafID
		base.ParentID = &parent
	}
	base.Timestamp = nowRFC3339()
	// 1.内存先于落盘变更，失败不回滚
	m.entries = append(m.entries, e)
	m.byID[base.ID] = e
	m.leafID = base.ID
	// 2.未 flush 时检查是否已有对话消息，有则整体落盘
	return m.persist(e)
}

// persist 按 flush 状态分流：未 flush 时检查触发条件，已 flush 时逐行追加
func (m *SessionManager) persist(e Entry) error {
	if !m.flushed {
		if !hasConversation(m.entries) {
			return nil
		}
		return m.flush()
	}
	// 3.上次追加失败的 entry 先原样重试
	if m.unwritten != nil {
		if err := m.appendLine(m.unwritten); err != nil {
			return err
		}
		m.unwritten = nil
	}
	if err := m.appendLine(e); err != nil {
		m.unwritten = e
		return err
	}
	return nil
}

// flush 独占创建会话文件并写入 header 与全部 entry；中途失败不清理不重写
func (m *SessionManager) flush() error {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return fmt.Errorf("session: 创建会话目录失败: %w", err)
	}
	f, err := os.OpenFile(m.sessionFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("session: 创建会话文件失败: %w", err)
	}
	defer f.Close()
	// 1.写 header
	headerLine, err := json.Marshal(m.header)
	if err != nil {
		return fmt.Errorf("session: 编码 header 失败: %w", err)
	}
	if _, err := f.Write(append(headerLine, '\n')); err != nil {
		return fmt.Errorf("session: 写入会话文件失败: %w", err)
	}
	// 2.按文件序写 entry，每行一个 JSON 对象
	for _, e := range m.entries {
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("session: 编码 entry 失败: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("session: 写入会话文件失败: %w", err)
		}
	}
	m.flushed = true
	return nil
}

// appendLine 向已存在的会话文件追加一行；末尾缺换行时先补写，部分写失败回滚到追加前长度
func (m *SessionManager) appendLine(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("session: 编码 entry 失败: %w", err)
	}
	f, err := os.OpenFile(m.sessionFile, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("session: 打开会话文件失败: %w", err)
	}
	defer f.Close()
	// 1.记录追加前长度，用于部分写失败时回滚
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("session: 获取会话文件大小失败: %w", err)
	}
	// 2.末尾缺换行先补一个，防止新行与残行粘连成一行
	buf := make([]byte, 0, len(line)+2)
	if m.needsNewline {
		buf = append(buf, '\n')
	}
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if _, err := f.Write(buf); err != nil {
		// 3.部分写失败回滚到追加前状态，缺换行标志保持不变，重试时整行重写
		if rollbackErr := f.Truncate(info.Size()); rollbackErr != nil {
			return fmt.Errorf("session: 写入会话文件失败: %w; 回滚失败: %v", err, rollbackErr)
		}
		return fmt.Errorf("session: 写入会话文件失败: %w", err)
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
	default:
		return ""
	}
}

// hasConversation 判断是否已有 user 或 assistant 消息，作为首次落盘的触发条件
func hasConversation(entries []Entry) bool {
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
