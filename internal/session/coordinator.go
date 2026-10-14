package session

import (
	"context"
	"errors"
	"sync"

	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/prompt"
)

// Runner 是编排层驱动 Agent 所需的能力，由 *agent.Agent 实现
type Runner interface {
	Run(ctx context.Context, input string) (*llm.AssistantMessage, error)
	// Continue 在不追加用户消息的情况下重入运行循环
	Continue(ctx context.Context) (*llm.AssistantMessage, error)
	// ReplaceMessages 用投影消息替换 Agent 内存消息序列（Tools 不变）
	ReplaceMessages(messages []llm.Message)
	// AppendMessage 把服务端事件之外的消息压入 Agent 内存消息序列
	AppendMessage(msg llm.Message)
}

// 压缩原因，随 compaction 事件暴露给消费者
const (
	CompactionReasonAuto     = "auto"
	CompactionReasonOverflow = "overflow"
	CompactionReasonManual   = "manual"
)

// Coordinator 会话编排层：负责事件落盘、压缩编排与溢出恢复驱动。
// 持久化与投影重建是直调，转发的事件退化为通知（对应 pi 的 AgentSession 角色）
type Coordinator struct {
	m    *SessionManager
	next llm.AgentEventSink

	// Agent 为被编排的运行器，由装配层在创建后设置
	Agent Runner
	// Compactor 为 nil 时禁用压缩
	Compactor *compaction.Compactor

	// pendingRecovery 记录正在收场的运行是否以可恢复的溢出结束，用于标注 agent_end 的 WillRetry
	pendingRecovery bool
	// recovered 限制每次 Run 最多做一次溢出恢复
	recovered bool
	// promptSections 是当前基线分节，由装配层设置、Reload 时替换；请求前与重放结果 diff
	promptSections prompt.Sections
	// mu 串行化 Run 与手动 Compact，避免压缩与进行中的运行并发读写会话与内存
	mu sync.Mutex
}

// NewCoordinator 创建编排层：next 为事件转发链（CLI 显示等）
func NewCoordinator(m *SessionManager, next llm.AgentEventSink) *Coordinator {
	return &Coordinator{m: m, next: next}
}

// SetPromptSections 设置基线分节；装配与 Reload 时调用，不写 system 消息。
// 加锁与 preparePrompt 的读取互斥，避免并发路径（如 TUI 事件回调）下数据竞争
func (c *Coordinator) SetPromptSections(sections prompt.Sections) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.promptSections = sections
}

// PromptSections 返回当前基线分节
func (c *Coordinator) PromptSections() prompt.Sections {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.promptSections
}

// preparePrompt 在进入 Agent 之前对齐 system prompt：把重放得到的当前分节与基线 diff，
// 有差异时先落盘再压入内存；transcript 只增不改，无差异不产生任何条目
func (c *Coordinator) preparePrompt() error {
	if len(c.promptSections) == 0 {
		return nil
	}
	var current prompt.Sections
	if existing := llm.CurrentSystemMessage(c.m.BuildContext().Messages()); existing != nil {
		current = existing.Sections
	}
	patch := prompt.DiffSections(current, c.promptSections)
	if patch == nil {
		return nil
	}
	message := llm.SystemMessage{Sections: patch}
	if err := c.m.AppendMessage(message); err != nil {
		return err
	}
	c.Agent.AppendMessage(message)
	return nil
}

// Handle 处理 agent 事件：转发在前（显示不依赖落盘成功，对齐 pi 的 _emit 顺序），
// message_end 落盘在后，落盘失败返回错误以终止当前 Agent 调用
func (c *Coordinator) Handle(event llm.AgentEvent) error {
	if end, ok := event.(llm.AgentEndEvent); ok {
		end.WillRetry = c.pendingRecovery && !c.recovered && c.Compactor != nil
		event = end
	}
	if err := c.emit(event); err != nil {
		return err
	}
	if e, ok := event.(llm.MessageEndEvent); ok {
		if err := c.m.AppendMessage(e.Message); err != nil {
			return err
		}
	}
	return nil
}

// Run 驱动一次运行；本轮以可恢复的溢出收场时，省略失败尝试、压缩后重试一次
func (c *Coordinator) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingRecovery, c.recovered = false, false
	// 0.请求前对齐 system prompt，变更以补丁条目追加
	if err := c.preparePrompt(); err != nil {
		return nil, err
	}
	reply, runErr := c.Agent.Run(ctx, input)
	// 无压缩器时无法恢复（窗口未知等场景）：按原始错误收场，失败尝试保留在会话中
	if !c.pendingRecovery || c.recovered || c.Compactor == nil {
		return reply, runErr
	}
	c.recovered = true
	// 1.省略失败尝试：它必然是最后落盘的 entry，磁盘上保留、投影上排除；失败原因不吞掉
	if oerr := c.omitLastAttempt(); oerr != nil {
		if runErr == nil {
			runErr = oerr
		}
		return reply, runErr
	}
	// 2.省略后立即重建内存，保持内存与投影一致（失败尝试从内存消失）
	c.Agent.ReplaceMessages(c.m.BuildContext().Messages())
	// 3.压缩成功才重试该轮；失败按原始错误收场
	if cerr := c.compact(ctx, CompactionReasonOverflow, ""); cerr != nil {
		if runErr == nil {
			runErr = cerr
		}
		return reply, runErr
	}
	c.pendingRecovery = false
	return c.Agent.Continue(ctx)
}

// Compact 手动触发一次压缩，instructions 为聚焦指令；与 Run 互斥避免并发读写
func (c *Coordinator) Compact(ctx context.Context, instructions string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.compact(ctx, CompactionReasonManual, instructions)
}

// CompactIfNeeded 实现 agent.CompactionCoordinator：在会话投影上评估静默溢出与阈值，
// 需要时压缩并替换内存；失败返回错误，Agent 对取消/超时中止、其余降级继续
func (c *Coordinator) CompactIfNeeded(ctx context.Context) (bool, error) {
	if c.Compactor == nil {
		return false, nil
	}
	proj := c.m.BuildContext()
	messages := proj.Messages()
	window := c.Compactor.Config.ContextWindow
	if window <= 0 {
		return false, nil
	}
	// 静默溢出优先：响应成功但最后一条 assistant 的 usage 已超窗
	if !proj.UsageStale {
		if msg := lastAssistant(messages); msg != nil && msg.Usage.InputTokens > window {
			if err := c.compact(ctx, CompactionReasonOverflow, ""); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	if compaction.ShouldCompact(contextTokens(proj), window, c.Compactor.Config.ReserveTokens) {
		if err := c.compact(ctx, CompactionReasonAuto, ""); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// NotifyOverflow 实现 agent.CompactionCoordinator：记录正在收场的运行以可恢复溢出结束
func (c *Coordinator) NotifyOverflow() {
	c.pendingRecovery = true
}

// compact 执行一次压缩：1.投影上找切点并摘要；2.直调落盘压缩点；3.重建投影并替换内存。
// 落盘成功前内存不替换，失败时会话与内存保持一致
func (c *Coordinator) compact(ctx context.Context, reason, instructions string) error {
	if c.Compactor == nil {
		return errors.New("session: context compaction not enabled")
	}
	proj := c.m.BuildContext()
	tokensBefore := contextTokens(proj)
	if err := c.emit(llm.CompactionStartEvent{Reason: reason, TokensBefore: tokensBefore}); err != nil {
		return err
	}
	result, err := c.Compactor.Compact(ctx, proj, instructions)
	if err != nil {
		// 失败通知，转发失败不影响状态
		_ = c.emit(llm.CompactionEndEvent{Reason: reason, TokensBefore: tokensBefore, Error: err.Error()})
		return err
	}
	var usage *llm.Usage
	if result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 0 {
		usage = &result.Usage
	}
	if err := c.m.AppendCompaction(result.Summary, result.FirstKeptSourceID, tokensBefore, llm.CurrentSystemMessage(proj.Messages()), usage); err != nil {
		return err
	}
	// 落盘成功后重建投影并提交内存；此后通知失败不影响状态
	rebuilt := c.m.BuildContext()
	c.Agent.ReplaceMessages(rebuilt.Messages())
	_ = c.emit(llm.CompactionEndEvent{
		Reason:           reason,
		Summary:          result.Summary,
		KeptMessageCount: result.KeptMessageCount,
		TokensBefore:     tokensBefore,
		TokensAfter:      compaction.EstimateContextTokens(rebuilt.Messages()),
		Usage:            result.Usage,
	})
	return nil
}

// omitLastAttempt 为最后落盘的失败尝试追加 context_edit 省略：磁盘上保留、投影上排除
func (c *Coordinator) omitLastAttempt() error {
	target := c.m.LastEntryID()
	if target == "" {
		return errors.New("session: no failed attempt to omit")
	}
	return c.m.AppendContextEdit(target, nil)
}

// emit 转发事件给下游消费者
func (c *Coordinator) emit(event llm.AgentEvent) error {
	if c.next == nil {
		return nil
	}
	return c.next(event)
}

// contextTokens 按陈旧标记选择 token 估算方式：陈旧时保留尾部的 usage 反映压缩前的大上下文，
// 改用纯消息体估算防止压缩刚结束就重复触发

func contextTokens(proj compaction.ContextView) int {
	if proj.UsageStale {
		return compaction.EstimateContextTokens(proj.Messages())
	}
	return compaction.ContextTokens(proj.Messages())
}

// lastAssistant 返回消息序列中最后一条 assistant 消息
func lastAssistant(messages []llm.Message) *llm.AssistantMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		if msg, ok := messages[i].(llm.AssistantMessage); ok {
			return &msg
		}
	}
	return nil
}
