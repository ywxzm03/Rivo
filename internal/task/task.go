package task

import (
	"context"
	"fmt"
	"time"

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

type StopReason string

const (
	StopReasonUserStop StopReason = "user_stop"
	StopReasonAborted  StopReason = "aborted"
	StopReasonShutdown StopReason = "shutdown"
)

// StoppedAgentSummary 生成父 agent 可直接理解的停止摘要
func StoppedAgentSummary(description string, reason StopReason) string {
	switch reason {
	case StopReasonUserStop:
		return fmt.Sprintf("Agent %q was stopped by user", description)
	case StopReasonAborted:
		return fmt.Sprintf("Agent %q was aborted", description)
	case StopReasonShutdown:
		return fmt.Sprintf("Agent %q was stopped during shutdown", description)
	default:
		return fmt.Sprintf("Agent %q was stopped", description)
	}
}

// StateBase 是 BashState 与 AgentState 共享的公共 lifecycle 状态
type StateBase struct {
	ID          string
	Type        Type
	Status      Status
	Description string
	ToolUseID   string
	StartTime   time.Time
	EndTime     *time.Time
	OutputFile  string
	// Notified 表示通知已入队,用于去重
	Notified bool
	// NotificationDelivered 表示通知已落盘进主会话,此后才允许驱逐任务行
	NotificationDelivered bool
	LifecycleToken        uint64
	StopReason            StopReason

	// EvictAfter 是终态任务的驱逐截止时间。nil 表示通知投递后立即可驱逐;
	// agent 任务用它实现面板宽限期
	EvictAfter *time.Time
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
	Command string
	Result  *BashResult
	Process BashProcess
	// ParentAgentID 是发起该后台任务的子 agent,主 agent 留空。
	// 子 agent 结束时按它回收孤儿进程
	ParentAgentID   string
	OutputTruncated bool
	OutputBytes     int
}

func (*BashState) taskState()         {}
func (s *BashState) Base() *StateBase { return &s.StateBase }

type AgentProgress struct {
	ToolUseCount int
	// TokenCount 是展示用的累计值:最新一次 input + 历次 output 之和。
	// input token 在单轮内已包含全部历史,累加会重复计数
	TokenCount        int
	LatestInputTokens int
	LastActivity      *ToolActivity
	RecentActivities  []ToolActivity
	Summary           string
}

type ToolActivity struct {
	ToolName            string
	ActivityDescription string
}

type AgentState struct {
	StateBase
	AgentID      string
	Prompt       string
	AgentType    string
	Model        *llm.Model
	Thinking     llm.ThinkingLevel
	WorktreePath string
	Worktree     bool
	Error        string
	Result       *AgentResult
	Progress     *AgentProgress
	Abort        context.CancelFunc
	// PendingSteering 在下一轮工具调用前注入;PendingFollowUp 在当前轮结束后注入。
	// 与主 agent 的两条队列语义一致
	PendingSteering []llm.UserMessage
	PendingFollowUp []llm.UserMessage
	Retain          bool
	// MessageCount 是 sidechain transcript 已提交的消息数。transcript 常驻磁盘,
	// 内存只保留这个长度,供 TUI 判断是否需要增量读取
	MessageCount int
}

func (*AgentState) taskState()         {}
func (s *AgentState) Base() *StateBase { return &s.StateBase }

// Runtime 是任务存储持有的运行时句柄。
// store 只用它做取消、存活等待与元数据回填，不认识具体 agent 类型，
// 具体实现在 subagent 包，避免 task 反向依赖 agent
type Runtime interface {
	// Abort 返回取消函数，store 挂到 AgentState.Abort 上供后续 kill 使用
	Abort() context.CancelFunc
	// Done 在 lifecycle 收尾时关闭，供 Shutdown 等待
	Done() <-chan struct{}
	// SetToken 由 store 在注册时回写 lifecycle token
	SetToken(token uint64)
	// OutputPath 是 sidechain transcript 落盘路径
	OutputPath() string
	// Worktree 是子 agent 的工作树路径，未启用时为空
	Worktree() string
}

type AgentInput struct {
	Description  string             `json:"description"`
	Prompt       string             `json:"prompt"`
	SubagentType *string            `json:"subagent_type,omitempty"`
	Worktree     *bool              `json:"worktree,omitempty"`
	Thinking     *llm.ThinkingLevel `json:"thinking,omitempty"`
	Model        *llm.Model         `json:"model,omitempty"`
	// ForkChild 标记调用方本身是 fork 子 agent。fork 保留 Agent 工具是为了让
	// 工具定义与父缓存一致,因此递归派发必须在调用边界拒绝
	ForkChild bool `json:"-"`
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

func (AgentLaunched) ToolDetails() {}

func init() { llm.RegisterToolDetails[AgentLaunched]("agentId") }

type AgentResult struct {
	AgentID           string      `json:"agentId"`
	Content           []TextBlock `json:"content"`
	TotalToolUseCount int         `json:"totalToolUseCount"`
	TotalDurationMs   int64       `json:"totalDurationMs"`
	TotalTokens       int         `json:"totalTokens"`
	Usage             llm.Usage   `json:"usage"`
}

type Notification struct {
	TaskID          string     `json:"task_id"`
	ToolUseID       string     `json:"tool_use_id,omitempty"`
	Reason          StopReason `json:"reason,omitempty"`
	Status          Status     `json:"status"`
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

type StopInput struct {
	TaskID string `json:"task_id"`
}

type StopOutput struct {
	Message string `json:"message"`
	TaskID  string `json:"task_id"`
	Type    Type   `json:"task_type"`
	Command string `json:"command"`
}

func (StopOutput) ToolDetails() {}

func init() { llm.RegisterToolDetails[StopOutput]("task_type") }

type StopTaskError struct {
	Code    string
	Message string
}

func (e *StopTaskError) Error() string { return e.Message }

// ResumeSource 是磁盘侧描述,指向已不在易失 store 中的任务
type ResumeSource struct {
	ID           string
	AgentType    string
	Description  string
	OutputFile   string
	WorktreePath string
	// WorktreeOwned 表示该 worktree 由子 agent 创建。恢复路径只记录该信息,
	// 不会据此安装清理:恢复后的会话会把用户工作写进 worktree
	WorktreeOwned bool
}

// ResumeResolver 查找已被 store 淘汰的任务,false 表示 transcript 已不存在
type ResumeResolver func(taskID string) (ResumeSource, bool)

// AgentSpec 是构造子 agent runtime 所需的完整描述
type AgentSpec struct {
	ID              string
	Description     string
	Prompt          string
	AgentType       string
	WorktreePath    string
	Worktree        bool
	PendingMessages []llm.UserMessage
	Model           llm.Model
	Thinking        llm.ThinkingLevel
	Continuation    bool
	ParentContext   *llm.Context
	ParentConfig    *llm.AgentConfig
}
