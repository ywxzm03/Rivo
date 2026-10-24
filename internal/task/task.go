package task

import (
	"context"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// Type 标识 Rivo 支持的两种后台任务实现
type Type string

const (
	TypeBash  Type = "bash"
	TypeAgent Type = "agent"
)

type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusKilled    Status = "killed"
)

func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusKilled
}

// StateBase 是 BashState 与 AgentState 共享的公共 lifecycle 状态
type StateBase struct {
	ID             string
	Type           Type
	Status         Status
	Description    string
	ToolUseID      string
	StartTime      time.Time
	EndTime        *time.Time
	TotalPausedMs  *int64
	OutputFile     string
	OutputOffset   int64
	Notified       bool
	LifecycleToken uint64
}

type State interface {
	taskState()
	Base() *StateBase
}

type BashResult struct {
	Code        int  `json:"code"`
	Interrupted bool `json:"interrupted"`
}

// BashProcess 是后台 bash 任务中由进程拥有的部分
type BashProcess interface {
	Kill() error
	Cleanup() error
}

type BashState struct {
	StateBase
	Command         string
	Result          *BashResult
	Process         BashProcess
	ParentAgentID   string
	Unregister      func()
	CleanupTimeout  *time.Timer
	LastOutputLines int
	OutputTruncated bool
	OutputBytes     int
}

func (*BashState) taskState()         {}
func (s *BashState) Base() *StateBase { return &s.StateBase }

type AgentProgress struct {
	ToolUseCount     int
	TokenCount       int
	LastActivity     *ToolActivity
	RecentActivities []ToolActivity
	Summary          string
}

type ToolActivity struct {
	ToolName            string
	ActivityDescription string
	IsSearch            bool
	IsRead              bool
}

type AgentState struct {
	StateBase
	AgentID             string
	Prompt              string
	AgentType           string
	SelectedAgent       string
	Model               *llm.Model
	Thinking            llm.ThinkingLevel
	WorktreePath        string
	Worktree            bool
	Error               string
	Result              *AgentResult
	Progress            *AgentProgress
	Abort               context.CancelFunc
	UnregisterCleanup   func()
	PendingMessages     []llm.UserMessage
	Retain              bool
	DiskLoaded          bool
	Messages            []llm.Message
	ContentReplacements map[string][]llm.Content
	EvictAfter          *time.Time
}

func (*AgentState) taskState()         {}
func (s *AgentState) Base() *StateBase { return &s.StateBase }

type AgentRuntime struct {
	Agent                   *agent.Agent
	Cancel                  context.CancelFunc
	Done                    chan struct{}
	LifecycleToken          uint64
	Cwd                     string
	OutputFile              string
	WorktreePath            string
	WorktreeCleanup         func() bool
	ContentReplacementState map[string][]llm.Content
	AppendMessages          func([]llm.Message) error
	Run                     func(context.Context, string) (*llm.AssistantMessage, error)
}

type AgentInput struct {
	Description  string             `json:"description"`
	Prompt       string             `json:"prompt"`
	Name         string             `json:"name,omitempty"`
	SubagentType *string            `json:"subagent_type,omitempty"`
	Worktree     *bool              `json:"worktree,omitempty"`
	Thinking     *llm.ThinkingLevel `json:"thinking,omitempty"`
	Model        *llm.Model         `json:"model,omitempty"`
}

type TextBlock struct {
	Text string `json:"text"`
}

type AgentLaunched struct {
	Status       string `json:"status"`
	AgentID      string `json:"agentId"`
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagentType"`
	State        Status `json:"state"`
	OutputFile   string `json:"outputFile"`
	WorktreePath string `json:"worktreePath,omitempty"`
}

type AgentResult struct {
	AgentID           string      `json:"agentId"`
	AgentType         string      `json:"agentType,omitempty"`
	Content           []TextBlock `json:"content"`
	TotalToolUseCount int         `json:"totalToolUseCount"`
	TotalDurationMs   int64       `json:"totalDurationMs"`
	TotalTokens       int         `json:"totalTokens"`
	Usage             llm.Usage   `json:"usage"`
}

type Notification struct {
	TaskID          string `json:"task_id"`
	ToolUseID       string `json:"tool_use_id,omitempty"`
	Status          Status `json:"status"`
	OutputFile      string `json:"output_file"`
	Summary         string `json:"summary"`
	Result          string `json:"result,omitempty"`
	TotalTokens     int    `json:"total_tokens,omitempty"`
	ToolUses        int    `json:"tool_uses,omitempty"`
	DurationMs      int64  `json:"duration_ms,omitempty"`
	WorktreePath    string `json:"worktree_path,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	OutputBytes     int    `json:"output_bytes,omitempty"`
}

type StopInput struct {
	TaskID string `json:"task_id"`
}

type StopOutput struct {
	Message string `json:"message"`
	TaskID  string `json:"task_id"`
	Type    Type   `json:"task_type"`
	Command string `json:"command"`
}

type StopTaskError struct {
	Code    string
	Message string
}

func (e *StopTaskError) Error() string { return e.Message }

type Task interface {
	Name() string
	Type() Type
	Kill(taskID string, store *Store) error
}

// ResumeSource 是磁盘侧描述,指向已不在易失 store 中的任务
type ResumeSource struct {
	ID           string
	AgentType    string
	Description  string
	OutputFile   string
	WorktreePath string
}

// ResumeResolver 查找已被 store 淘汰的任务,false 表示 transcript 已不存在
type ResumeResolver func(taskID string) (ResumeSource, bool)

// AgentSpec 是构造子 agent runtime 所需的完整描述
type AgentSpec struct {
	ID              string
	Description     string
	Prompt          string
	AgentType       string
	ToolUseID       string
	OutputFile      string
	WorktreePath    string
	Worktree        bool
	PendingMessages []llm.UserMessage
	InitialMessages []llm.Message
	Model           llm.Model
	Thinking        llm.ThinkingLevel
	Continuation    bool
	ParentContext   *llm.Context
	ParentConfig    *llm.AgentConfig
}
