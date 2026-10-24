package subagent

import (
	"context"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// TaskType 标识 Rivo 支持的两种后台任务实现
type TaskType string

const (
	TaskTypeBash  TaskType = "bash"
	TaskTypeAgent TaskType = "agent"
)

type TaskStatus string

const (
	StateRunning   TaskStatus = "running"
	StateCompleted TaskStatus = "completed"
	StateFailed    TaskStatus = "failed"
	StateKilled    TaskStatus = "killed"
)

func (s TaskStatus) Terminal() bool {
	return s == StateCompleted || s == StateFailed || s == StateKilled
}

// TaskStateBase 是 BashTaskState 与 AgentTaskState 共享的公共 lifecycle 状态
type TaskStateBase struct {
	ID             string
	Type           TaskType
	Status         TaskStatus
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

type TaskState interface {
	taskState()
	Base() *TaskStateBase
}

type BashTaskResult struct {
	Code        int  `json:"code"`
	Interrupted bool `json:"interrupted"`
}

// BashProcess 是后台 bash 任务中由进程拥有的部分
type BashProcess interface {
	Kill() error
	Cleanup() error
}

type BashTaskState struct {
	TaskStateBase
	Command         string
	Result          *BashTaskResult
	Process         BashProcess
	ParentAgentID   string
	Unregister      func()
	CleanupTimeout  *time.Timer
	LastOutputLines int
	OutputTruncated bool
	OutputBytes     int
}

func (*BashTaskState) taskState()             {}
func (s *BashTaskState) Base() *TaskStateBase { return &s.TaskStateBase }

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

type AgentTaskState struct {
	TaskStateBase
	AgentID             string
	Prompt              string
	AgentType           string
	SelectedAgent       string
	Model               *llm.Model
	Thinking            llm.ThinkingLevel
	WorktreePath        string
	Worktree            bool
	Error               string
	Result              *AgentToolResult
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

func (*AgentTaskState) taskState()             {}
func (s *AgentTaskState) Base() *TaskStateBase { return &s.TaskStateBase }

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

type AgentToolInput struct {
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

type AgentToolLaunched struct {
	Status       string     `json:"status"`
	AgentID      string     `json:"agentId"`
	Description  string     `json:"description"`
	Prompt       string     `json:"prompt"`
	SubagentType string     `json:"subagentType"`
	State        TaskStatus `json:"state"`
	OutputFile   string     `json:"outputFile"`
	WorktreePath string     `json:"worktreePath,omitempty"`
}

type AgentToolResult struct {
	AgentID           string      `json:"agentId"`
	AgentType         string      `json:"agentType,omitempty"`
	Content           []TextBlock `json:"content"`
	TotalToolUseCount int         `json:"totalToolUseCount"`
	TotalDurationMs   int64       `json:"totalDurationMs"`
	TotalTokens       int         `json:"totalTokens"`
	Usage             llm.Usage   `json:"usage"`
}

type TaskNotification struct {
	TaskID          string     `json:"task_id"`
	ToolUseID       string     `json:"tool_use_id,omitempty"`
	Status          TaskStatus `json:"status"`
	OutputFile      string     `json:"output_file"`
	Summary         string     `json:"summary"`
	Result          string     `json:"result,omitempty"`
	TotalTokens     int        `json:"total_tokens,omitempty"`
	ToolUses        int        `json:"tool_uses,omitempty"`
	DurationMs      int64      `json:"duration_ms,omitempty"`
	WorktreePath    string     `json:"worktree_path,omitempty"`
	OutputTruncated bool       `json:"output_truncated,omitempty"`
	OutputBytes     int        `json:"output_bytes,omitempty"`
}

type TaskStopInput struct {
	TaskID string `json:"task_id"`
}

type TaskStopOutput struct {
	Message  string   `json:"message"`
	TaskID   string   `json:"task_id"`
	TaskType TaskType `json:"task_type"`
	Command  string   `json:"command"`
}

type StopTaskError struct {
	Code    string
	Message string
}

func (e *StopTaskError) Error() string { return e.Message }

type Task interface {
	Name() string
	Type() TaskType
	Kill(taskID string, store *Store) error
}
