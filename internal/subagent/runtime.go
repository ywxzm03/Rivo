package subagent

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// AgentRuntime 是一次子 agent lifecycle 的运行时句柄。
// 它持有活的 Agent、取消函数、完成信号与派发入口，
// 由 agenttool 构造，交给 task.Store 通过 task.Runtime 接口管理
type AgentRuntime struct {
	Agent           *agent.Agent
	Cancel          context.CancelFunc
	DoneChan        chan struct{}
	LifecycleToken  uint64
	OutputFile      string
	WorktreePath    string
	WorktreeCleanup func() bool
	Run             func(context.Context, string) (*llm.AssistantMessage, error)
	// MessageStart 是本 lifecycle 起始的消息下标。fork 会继承父 transcript,
	// 统计用量时必须从这里开始,否则会把父对话算进子任务
	MessageStart int
}

// Abort 返回取消函数，未设置时返回 nil
func (r *AgentRuntime) Abort() context.CancelFunc { return r.Cancel }

// Done 返回 lifecycle 完成信号，未初始化时返回 nil
func (r *AgentRuntime) Done() <-chan struct{} { return r.DoneChan }

// SetToken 由 task.Store 在注册时回写 lifecycle token
func (r *AgentRuntime) SetToken(token uint64) { r.LifecycleToken = token }

// OutputPath 是 sidechain transcript 落盘路径
func (r *AgentRuntime) OutputPath() string { return r.OutputFile }

// Worktree 是子 agent 的工作树路径，未启用时为空
func (r *AgentRuntime) Worktree() string { return r.WorktreePath }

// compile-time check：AgentRuntime 必须满足 task.Runtime
var _ interface {
	Abort() context.CancelFunc
	Done() <-chan struct{}
	SetToken(uint64)
	OutputPath() string
	Worktree() string
} = (*AgentRuntime)(nil)
