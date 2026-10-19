// Package session 实现树形 JSONL 会话持久化：entry 按文件序追加，id/parentId 构成树，
// 当前叶子决定活跃分支，BuildContext 从叶子恢复 LLM 上下文
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	compact "github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
)

const CurrentVersion = 1

// entry 类型常量
const (
	TypeSession             = "session"
	TypeMessage             = "message"
	TypeModelChange         = "model_change"
	TypeThinkingLevelChange = "thinking_level_change"
	TypeUsage               = "usage"
	TypeCompaction          = "compaction"
	TypeContextEdit         = "context_edit"
	TypeSessionInfo         = "session_info"
	// file-history 记录是 JSONL metadata，不参与树结构
	TypeFileHistorySnapshot = filehistory.TypeSnapshot
	TypeFileHistoryDelta    = filehistory.TypeDelta
)

// Header 会话文件首行，不参与树结构
type Header struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

// EntryBase 除 header 外所有 entry 的公共字段，ParentID 为 nil 表示根 entry
type EntryBase struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
}

// Entry 可入树的会话条目；未知类型解码为 UnknownEntry，参与树路径但不产出 LLM 消息
type Entry interface {
	base() *EntryBase
}

// MessageEntry 一条 user/assistant/toolResult 消息
type MessageEntry struct {
	EntryBase
	Message llm.Message
}

// ModelChangeEntry 中途切换模型
type ModelChangeEntry struct {
	EntryBase
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// ThinkingLevelChangeEntry 改变思考级别
type ThinkingLevelChangeEntry struct {
	EntryBase
	ThinkingLevel string `json:"thinkingLevel"`
}

// SessionInfoEntry 保存整个 Thread 的显示名称，不参与模型上下文
type SessionInfoEntry struct {
	EntryBase
	Name string `json:"name"`
}

// Usage 不依附消息的 token 记录，计入会话用量但不进 LLM 上下文
type Usage struct {
	InputTokens        int       `json:"inputTokens"`
	OutputTokens       int       `json:"outputTokens"`
	CacheReadTokens    int       `json:"cacheReadTokens"`
	CacheWriteTokens   int       `json:"cacheWriteTokens"`
	CacheWrite1HTokens int       `json:"cacheWrite1hTokens"`
	ReasoningTokens    int       `json:"reasoningTokens"`
	TotalTokens        int       `json:"totalTokens"`
	Cost               *llm.Cost `json:"cost,omitempty"`
}

// UsageEntry 记录模型用量（如缓存预热），不参与 LLM 上下文
type UsageEntry struct {
	EntryBase
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Usage    Usage  `json:"usage"`
	Note     string `json:"note,omitempty"`
}

// CompactionEntry 上下文压缩点
type CompactionEntry struct {
	EntryBase
	Summary          string             `json:"summary"`
	FirstKeptEntryID string             `json:"firstKeptEntryId"`
	TokensBefore     int                `json:"tokensBefore"`
	Details          *compact.FileLists `json:"details,omitempty"`
	SystemMessage    *llm.SystemMessage `json:"systemMessage,omitempty"`
	Usage            *Usage             `json:"usage,omitempty"`
}

// cloneFileLists 复制结构化文件元数据，避免会话条目共享可变切片
func cloneFileLists(details *compact.FileLists) *compact.FileLists {
	if details == nil {
		return nil
	}
	return &compact.FileLists{
		ReadFiles:     append([]string{}, details.ReadFiles...),
		ModifiedFiles: append([]string{}, details.ModifiedFiles...),
	}
}

// Replacement context_edit 的替换内容，nil 表示省略目标 entry
type Replacement struct {
	Content []llm.Content `json:"content"`
}

// ContextEditEntry 只改未来模型看到的内容，原始历史不动
type ContextEditEntry struct {
	EntryBase
	TargetID    string       `json:"targetId"`
	Replacement *Replacement `json:"replacement"`
}

// UnknownEntry 未知类型的 entry，仅保留树结构字段
type UnknownEntry struct {
	EntryBase
}

func (b *EntryBase) base() *EntryBase { return b }

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("session: failed to generate session ID: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ValidateID 校验 --session-id 接受的稳定标识符
func ValidateID(id string) error {
	runes := []rune(id)
	if len(runes) == 0 {
		return errors.New("session: id must be non-empty")
	}
	for i, r := range runes {
		if !isAlphaNumericRune(r) && !strings.ContainsRune("-_.", r) {
			return errors.New("session: id may contain only letters, numbers, '-', '_' and '.'")
		}
		if (i == 0 || i == len(runes)-1) && !isAlphaNumericRune(r) {
			return errors.New("session: id must start and end with a letter or number")
		}
	}
	if isWindowsReservedID(id) {
		return errors.New("session: id is reserved on Windows")
	}
	return nil
}

func isAlphaNumericRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func isWindowsReservedID(id string) bool {
	base := strings.ToLower(strings.SplitN(id, ".", 2)[0])
	switch base {
	case "con", "prn", "aux", "nul", "com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9", "lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		return true
	default:
		return false
	}
}

func randomHexID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("session: failed to generate entry ID: %v", err))
	}
	return hex.EncodeToString(b[:])
}
