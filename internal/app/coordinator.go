package app

import (
	"context"
	"errors"
	"fmt"
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
	Continue(ctx context.Context) (*llm.AssistantMessage, error)
	Steer(input string) error
	SteerMessage(llm.UserMessage) error
	FollowUpMessage(llm.UserMessage) error
}

type queuedRunner interface {
	ContinueSteering(context.Context) (*llm.AssistantMessage, error)
}

type followUpRunner interface {
	ContinueFollowUp(context.Context) (*llm.AssistantMessage, error)
}

var ErrSteeringDuringCompaction = errors.New("app: steering is unavailable during context compaction")

var ErrFollowUpDuringCompaction = errors.New("app: follow-up is unavailable during context compaction")

var ErrQueuedRunnerUnavailable = errors.New("app: queued runner is unavailable")

// 压缩原因，随 compaction 事件暴露给消费者
const (
	CompactionReasonAuto     = "auto"
	CompactionReasonOverflow = "overflow"
	CompactionReasonManual   = "manual"
)

// Coordinator 会话编排层：负责事件落盘、压缩编排与溢出恢复驱动。
// 持久化与投影重建是直调，转发的事件退化为通知
type Coordinator struct {
	m    *session.SessionManager
	next llm.AgentEventSink

	Agent Runner
	// Compactor 为 nil 时禁用压缩
	Compactor *compaction.Compactor
	// compaction 承载压缩编排与溢出恢复状态，Compactor 变更后需同步
	compaction *compactionController

	model llm.Model
	// promptSections 是当前基线分节，请求前与重放结果 diff
	promptSections prompt.Sections
	// mu 只保护基线分节；runMu 串行化编排
	mu    sync.Mutex
	runMu sync.Mutex
	// transientMessages 提供只在单次请求内出现的附件,例如子任务完成通知
	transientMessages func() []llm.Message
	// transientAck 在请求成功提交后确认这批附件已被消费
	transientAck func()
	taskEvictor  func() int
}

func NewCoordinator(m *session.SessionManager, next llm.AgentEventSink) *Coordinator {
	c := &Coordinator{m: m, next: next}
	c.compaction = newCompactionController(m, c.emit, func() *compaction.Compactor { return c.Compactor })
	return c
}

// setModel 同步当前运行模型
func (c *Coordinator) setModel(model llm.Model) {
	c.model = model
}

func (c *Coordinator) resetRecovery() {
	c.compaction.resetRecovery()
}

func (c *Coordinator) EnqueueSteering(input string) error {
	return c.EnqueueSteeringMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

// EnqueueSteeringMessage 在压缩状态锁内接受一条 steering，压缩开始后拒绝新输入
func (c *Coordinator) EnqueueSteeringMessage(input llm.UserMessage) error {
	if c.compaction.isCompacting() {
		return ErrSteeringDuringCompaction
	}
	if c.Agent == nil {
		return errors.New("app: steering runner is unavailable")
	}
	return c.Agent.SteerMessage(input)
}

func (c *Coordinator) EnqueueFollowUp(input string) error {
	return c.EnqueueFollowUpMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

// EnqueueFollowUpMessage 在压缩状态锁内接受一条 follow-up
func (c *Coordinator) EnqueueFollowUpMessage(input llm.UserMessage) error {
	if c.compaction.isCompacting() {
		return ErrFollowUpDuringCompaction
	}
	if c.Agent == nil {
		return errors.New("app: follow-up runner is unavailable")
	}
	return c.Agent.FollowUpMessage(input)
}

// SetPromptSections 设置基线分节；加锁与 preparePrompt 的读取互斥，避免并发路径下数据竞争
func (c *Coordinator) SetPromptSections(sections prompt.Sections) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.promptSections = sections
}

func (c *Coordinator) PromptSections() prompt.Sections {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.promptSections
}

// preparePrompt 在进入 Agent 之前对齐 system prompt：有差异时先落盘再压入内存，
// transcript 只增不改，无差异不产生任何条目
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
		// 发送给模型前建立 turn 起点；文件历史失败只记录降级状态
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
		c.compaction.pendingRecovery = overflow
		end.WillRetry = overflow && c.compaction.needsRecovery()
		event = end
	}
	return c.emit(event)
}

// PrepareRequest 返回会话投影快照，摘要生成失败时保留原上下文，存储失败必须停止运行
// SetTransientMessages 安装请求级消息提供器
func (c *Coordinator) SetTransientMessages(provider func() []llm.Message) {
	c.mu.Lock()
	c.transientMessages = provider
	c.mu.Unlock()
}

// SetTransientAck 安装请求成功后的确认回调
func (c *Coordinator) SetTransientAck(ack func()) {
	c.mu.Lock()
	c.transientAck = ack
	c.mu.Unlock()
}

// SetTaskEvictor 安装请求边界的终态任务回收器
func (c *Coordinator) SetTaskEvictor(evictor func() int) {
	c.mu.Lock()
	c.taskEvictor = evictor
	c.mu.Unlock()
}

func (c *Coordinator) takeTransientMessages() []llm.Message {
	c.mu.Lock()
	provider := c.transientMessages
	c.mu.Unlock()
	if provider == nil {
		return nil
	}
	return provider()
}

func (c *Coordinator) takeTransientAck() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transientAck
}

func (c *Coordinator) evictExpiredTasks() {
	c.mu.Lock()
	evictor := c.taskEvictor
	c.mu.Unlock()
	if evictor != nil {
		evictor()
	}
}

func (c *Coordinator) PrepareRequest(ctx context.Context, current agent.PreparedRequest) (agent.PreparedRequest, error) {
	c.setModel(current.Config.Model)
	// 请求边界顺带回收宽限期已过的终态子任务;通知队列不受影响
	c.evictExpiredTasks()
	compacted, err := c.CompactIfNeeded(ctx)
	if err != nil {
		var commitErr *session.CommitError
		if errors.As(err, &commitErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return current, err
		}
	}
	current.Context.Messages = c.m.BuildContext().Messages()
	current.Context.Messages = append(current.Context.Messages, c.takeTransientMessages()...)
	current.TransientAck = c.takeTransientAck()
	current.Compacted = compacted
	return current, nil
}

// Run 驱动一次文本运行
func (c *Coordinator) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	return c.RunMessage(ctx, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

// RunMessage 驱动一次运行；本轮以可恢复的溢出收场时，省略失败尝试、压缩后重试一次
func (c *Coordinator) RunMessage(ctx context.Context, input llm.UserMessage) (*llm.AssistantMessage, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	c.resetRecovery()
	// 1.请求前对齐 system prompt，变更以补丁条目追加
	if err := c.preparePrompt(); err != nil {
		return nil, err
	}
	if runner, ok := c.Agent.(interface {
		RunMessage(context.Context, llm.UserMessage) (*llm.AssistantMessage, error)
	}); ok {
		reply, runErr := runner.RunMessage(ctx, input)
		return c.finishRun(ctx, reply, runErr)
	}
	if userMessageHasImages(input) {
		return nil, errors.New("app: runner does not support structured image input")
	}
	reply, runErr := c.Agent.Run(ctx, llm.TextOf(input.Content))
	return c.finishRun(ctx, reply, runErr)
}

// Continue 续接当前会话并完成一次溢出恢复
func (c *Coordinator) Continue(ctx context.Context) (*llm.AssistantMessage, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	reply, runErr := c.Agent.Continue(ctx)
	return c.finishRun(ctx, reply, runErr)
}

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

func (c *Coordinator) ContinueFollowUp(ctx context.Context) (*llm.AssistantMessage, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if runner, ok := c.Agent.(followUpRunner); ok {
		reply, runErr := runner.ContinueFollowUp(ctx)
		return c.finishRun(ctx, reply, runErr)
	}
	return nil, ErrQueuedRunnerUnavailable
}

// finishRun 在操作结束前完成溢出恢复；持久化失败优先透出
func (c *Coordinator) finishRun(ctx context.Context, reply *llm.AssistantMessage, runErr error) (*llm.AssistantMessage, error) {
	if !c.compaction.needsRecovery() {
		return reply, runErr
	}
	c.compaction.recovered = true
	// 2.省略失败尝试：磁盘上保留、投影上排除；失败原因不吞掉
	if oerr := c.compaction.omitLastAttempt(); oerr != nil {
		return reply, errors.Join(runErr, oerr)
	}
	// 3.压缩成功才重试该轮；失败按原始错误收场
	if cerr := c.compaction.compact(ctx, CompactionReasonOverflow, ""); cerr != nil {
		var commitErr *session.CommitError
		if runErr == nil || errors.As(cerr, &commitErr) {
			runErr = cerr
		}
		return reply, runErr
	}
	c.compaction.pendingRecovery = false
	return c.Agent.Continue(ctx)
}

// retryOverflowRecovery 在省略标记补写成功后继续完成同一轮溢出恢复
func (c *Coordinator) retryOverflowRecovery(ctx context.Context, resume bool) (*llm.AssistantMessage, error) {
	if !c.compaction.pendingRecovery || c.compaction.compactor() == nil {
		return nil, nil
	}
	if err := c.compaction.compact(ctx, CompactionReasonOverflow, ""); err != nil {
		return nil, err
	}
	c.compaction.pendingRecovery = false
	if resume {
		return c.Agent.Continue(ctx)
	}
	return nil, nil
}

// Compact 手动触发一次压缩，instructions 为聚焦指令
func (c *Coordinator) Compact(ctx context.Context, instructions string) error {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	return c.compaction.compact(ctx, CompactionReasonManual, instructions)
}

// CompactIfNeeded 在会话投影上评估静默溢出与阈值，提交成功才发布压缩点
func (c *Coordinator) CompactIfNeeded(ctx context.Context) (bool, error) {
	reason, needed := c.compaction.shouldCompact(c.m.BuildContext())
	if !needed {
		return false, nil
	}
	if err := c.compaction.compact(ctx, reason, ""); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Coordinator) emit(event llm.AgentEvent) error {
	if c.next == nil {
		return nil
	}
	return c.next(event)
}
