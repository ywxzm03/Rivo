package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/session"
)

// Runner 是编排层驱动 Agent 所需的能力，由 *agent.Agent 实现
type Runner interface {
	Run(ctx context.Context, input string) (*llm.AssistantMessage, error)
	// Continue 在不追加用户消息的情况下重入运行循环
	Continue(ctx context.Context) (*llm.AssistantMessage, error)
	// Steer 把输入加入当前运行的 steering 队列
	Steer(input string) error
}

type queuedRunner interface {
	ContinueSteering(context.Context) (*llm.AssistantMessage, error)
}

var ErrSteeringDuringCompaction = errors.New("app: steering is unavailable during context compaction")

var ErrQueuedRunnerUnavailable = errors.New("app: queued steering runner is unavailable")

// 压缩原因，随 compaction 事件暴露给消费者
const (
	CompactionReasonAuto     = "auto"
	CompactionReasonOverflow = "overflow"
	CompactionReasonManual   = "manual"
)

// Coordinator 会话编排层：负责事件落盘、压缩编排与溢出恢复驱动。
// 持久化与投影重建是直调，转发的事件退化为通知（对应 pi 的 AgentSession 角色）
type Coordinator struct {
	m    *session.SessionManager
	next llm.AgentEventSink

	// Agent 为被编排的运行器，由装配层在创建后设置
	Agent Runner
	// Compactor 为 nil 时禁用压缩
	Compactor *compaction.Compactor

	// pendingRecovery 记录正在收场的运行是否以可恢复的溢出结束，用于标注 agent_end 的 WillRetry
	model           llm.Model
	pendingRecovery bool
	// recovered 限制每次 Run 最多做一次溢出恢复
	recovered bool
	// promptSections 是当前基线分节，由装配层设置、Reload 时替换；请求前与重放结果 diff
	promptSections prompt.Sections
	// mu 只保护基线分节；runMu 串行化编排，生命周期所有权由 App 管理
	mu         sync.Mutex
	runMu      sync.Mutex
	stateMu    sync.Mutex
	compacting bool
}

// NewCoordinator 创建编排层：next 为事件转发链（CLI 显示等）
func NewCoordinator(m *session.SessionManager, next llm.AgentEventSink) *Coordinator {
	return &Coordinator{m: m, next: next}
}

// setModel 同步当前运行模型，恢复路径也能使用最新的输出上限
func (c *Coordinator) setModel(model llm.Model) {
	c.model = model
}

// resetRecovery 开始重放已提交操作时重新建立本轮恢复状态
func (c *Coordinator) resetRecovery() {
	c.pendingRecovery, c.recovered = false, false
}

// EnqueueSteering 在压缩状态锁内接受一条 steering，压缩开始后拒绝新输入
func (c *Coordinator) EnqueueSteering(input string) error {
	return c.EnqueueSteeringMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

// EnqueueSteeringMessage 接受带图片的 steering 输入
func (c *Coordinator) EnqueueSteeringMessage(input llm.UserMessage) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.compacting {
		return ErrSteeringDuringCompaction
	}
	if c.Agent == nil {
		return errors.New("app: steering runner is unavailable")
	}
	if runner, ok := c.Agent.(interface{ SteerMessage(llm.UserMessage) error }); ok {
		return runner.SteerMessage(input)
	}
	return c.Agent.Steer(userMessageText(input))
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
	sections := c.PromptSections()
	if len(sections) == 0 {
		return nil
	}
	var current prompt.Sections
	if existing := llm.CurrentSystemMessage(c.m.BuildContext().Messages()); existing != nil {
		current = existing.Sections
	}
	patch := prompt.DiffSections(current, sections)
	if patch == nil {
		return nil
	}
	message := llm.SystemMessage{Sections: patch}
	if err := c.m.AppendMessage(message); err != nil {
		return err
	}
	return nil
}

// CommitMessages 是核心推进上下文前的持久化边界
func (c *Coordinator) CommitMessages(messages []llm.Message) ([]string, error) {
	ids, err := c.m.AppendMessages(messages)
	if err != nil {
		return ids, err
	}
	for index, message := range messages {
		if _, ok := message.(llm.UserMessage); !ok || index >= len(ids) || ids[index] == "" {
			continue
		}
		// 发送给模型前建立 turn 起点；文件历史失败只记录降级状态，不阻止模型运行
		if snapshotErr := c.m.MakeFileHistorySnapshot(ids[index]); snapshotErr != nil {
			warning := fmt.Sprintf("file history snapshot unavailable for %s: %v", ids[index], snapshotErr)
			c.m.AddWarning(warning)
			_ = c.emit(llm.WarningEvent{Message: warning})
		}
	}
	return ids, nil
}

// Handle 只转发运行通知；带 SourceID 的 message_end 已经成功落盘
func (c *Coordinator) Handle(event llm.AgentEvent) error {
	if end, ok := event.(llm.AgentEndEvent); ok {
		overflow := llm.IsContextOverflow(end.Err)
		if len(end.Messages) > 0 {
			if message, ok := end.Messages[len(end.Messages)-1].(llm.AssistantMessage); ok {
				overflow = overflow || (message.StopReason == llm.StopReasonLength && c.model.MaxTokens > 0 && message.Usage.OutputTokens < c.model.MaxTokens)
			}
		}
		c.pendingRecovery = overflow
		end.WillRetry = overflow && !c.recovered && c.Compactor != nil
		event = end
	}
	return c.emit(event)
}

// PrepareRequest 返回会话投影快照，压缩失败不改写核心字段
func (c *Coordinator) PrepareRequest(ctx context.Context, current agent.PreparedRequest) (agent.PreparedRequest, error) {
	c.setModel(current.Config.Model)
	compacted, err := c.CompactIfNeeded(ctx)
	if err != nil {
		var commitErr *session.CommitError
		if errors.As(err, &commitErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return current, err
		}
		// 摘要生成失败保留原上下文；存储失败必须停止运行
	}
	current.Context.Messages = c.m.BuildContext().Messages()
	current.Compacted = compacted
	return current, nil
}

// Run 驱动一次运行；本轮以可恢复的溢出收场时，省略失败尝试、压缩后重试一次
func (c *Coordinator) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	return c.RunMessage(ctx, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

// RunMessage 驱动一次包含文本和图片的运行
func (c *Coordinator) RunMessage(ctx context.Context, input llm.UserMessage) (*llm.AssistantMessage, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	c.resetRecovery()
	// 0.请求前对齐 system prompt，变更以补丁条目追加
	if err := c.preparePrompt(); err != nil {
		return nil, err
	}
	if runner, ok := c.Agent.(interface {
		RunMessage(context.Context, llm.UserMessage) (*llm.AssistantMessage, error)
	}); ok {
		reply, runErr := runner.RunMessage(ctx, input)
		return c.finishRun(ctx, reply, runErr)
	}
	reply, runErr := c.Agent.Run(ctx, userMessageText(input))
	return c.finishRun(ctx, reply, runErr)
}

func userMessageText(message llm.UserMessage) string {
	var parts []string
	for _, content := range message.Content {
		if text, ok := content.(llm.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Continue 续接当前会话并完成一次溢出恢复，供同一操作消费晚到的 steering
func (c *Coordinator) Continue(ctx context.Context) (*llm.AssistantMessage, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	reply, runErr := c.Agent.Continue(ctx)
	return c.finishRun(ctx, reply, runErr)
}

// ContinueQueued 只在 Runner 成功认领 steering 时续跑
func (c *Coordinator) ContinueQueued(ctx context.Context) (*llm.AssistantMessage, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	var reply *llm.AssistantMessage
	var runErr error
	if runner, ok := c.Agent.(queuedRunner); ok {
		reply, runErr = runner.ContinueSteering(ctx)
	} else {
		return nil, ErrQueuedRunnerUnavailable
	}
	return c.finishRun(ctx, reply, runErr)
}

// finishRun 在操作结束前完成溢出恢复；持久化失败优先透出
func (c *Coordinator) finishRun(ctx context.Context, reply *llm.AssistantMessage, runErr error) (*llm.AssistantMessage, error) {
	// 无压缩器时无法恢复（窗口未知等场景）：按原始错误收场，失败尝试保留在会话中
	if !c.pendingRecovery || c.recovered || c.Compactor == nil {
		return reply, runErr
	}
	c.recovered = true
	// 1.省略失败尝试：它必然是最后落盘的 entry，磁盘上保留、投影上排除；失败原因不吞掉
	if oerr := c.omitLastAttempt(); oerr != nil {
		return reply, errors.Join(runErr, oerr)
	}
	// 3.压缩成功才重试该轮；失败按原始错误收场
	if cerr := c.compact(ctx, CompactionReasonOverflow, ""); cerr != nil {
		var commitErr *session.CommitError
		if runErr == nil || errors.As(cerr, &commitErr) {
			runErr = cerr
		}
		return reply, runErr
	}
	c.pendingRecovery = false
	return c.Agent.Continue(ctx)
}

// retryOverflowRecovery 在省略标记补写成功后继续完成同一轮溢出恢复
func (c *Coordinator) retryOverflowRecovery(ctx context.Context, resume bool) (*llm.AssistantMessage, error) {
	if !c.pendingRecovery || c.Compactor == nil {
		return nil, nil
	}
	if err := c.compact(ctx, CompactionReasonOverflow, ""); err != nil {
		return nil, err
	}
	c.pendingRecovery = false
	if resume {
		return c.Agent.Continue(ctx)
	}
	return nil, nil
}

// Compact 手动触发一次压缩，instructions 为聚焦指令；与 Run 互斥避免并发读写
func (c *Coordinator) Compact(ctx context.Context, instructions string) error {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	return c.compact(ctx, CompactionReasonManual, instructions)
}

// CompactIfNeeded 在会话投影上评估静默溢出与阈值，提交成功才发布压缩点
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

// compact 先计算候选摘要，成功落盘后才发布新投影
// 落盘成功前内存不替换，失败时会话与内存保持一致
func (c *Coordinator) compact(ctx context.Context, reason, instructions string) error {
	if c.Compactor == nil {
		return errors.New("session: context compaction not enabled")
	}
	c.stateMu.Lock()
	c.compacting = true
	c.stateMu.Unlock()
	defer func() {
		c.stateMu.Lock()
		c.compacting = false
		c.stateMu.Unlock()
	}()
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
	if err := c.m.AppendCompactionWithDetails(result.Summary, result.FirstKeptSourceID, tokensBefore, llm.CurrentSystemMessage(proj.Messages()), usage, &result.FileLists); err != nil {
		_ = c.emit(llm.CompactionEndEvent{Reason: reason, TokensBefore: tokensBefore, Error: err.Error()})
		return err
	}
	// 落盘成功后重建投影并提交内存；此后通知失败不影响状态
	rebuilt := c.m.BuildContext()
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
