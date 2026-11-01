package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type Agent struct {
	cfg    llm.AgentConfig
	ctx    llm.Context
	stream llm.StreamFn
	events llm.AgentEventSink
	// workingDir 是工具执行的工作目录,子 agent 用它指向 worktree
	workingDir string
	// maxTurns 为 0 表示不限制;fork 与自定义定义用它封顶轮次
	maxTurns int
	// forkChild 标记该 agent 是 fork 子 agent,工具层据此拒绝递归派发
	forkChild bool
	// agentID 是子 agent 的任务 ID,主 agent 留空。工具层据此把后台任务
	// 归属到发起它的子 agent
	agentID string
	hooks   Hooks

	runGuardOnce  sync.Once
	runGuard      chan struct{}
	steeringQueue pendingMessageQueue
	followUpQueue pendingMessageQueue
	currentUserID string
	// retryableMu 保护 retryableQueue：只记录最近一次 commit 失败恢复的队列，
	// 供下一次操作按队列类型确认补写，避免同文本跨队列误删
	retryableMu    sync.Mutex
	retryableQueue *pendingMessageQueue
}

var ErrMaxTurns = errors.New("agent: maximum turns reached")

type pendingMessage struct {
	input     string
	message   llm.UserMessage
	retryable bool
}

type pendingMessageQueue struct {
	mu       sync.Mutex
	items    []pendingMessage
	revision uint64
}

func (q *pendingMessageQueue) append(message pendingMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, pendingMessage{input: message.input, message: cloneUserMessage(message.message), retryable: message.retryable})
	q.revision++
}

func (q *pendingMessageQueue) snapshot() []pendingMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := make([]pendingMessage, len(q.items))
	for i, item := range q.items {
		items[i] = pendingMessage{input: item.input, message: cloneUserMessage(item.message), retryable: item.retryable}
	}
	return items
}

func (q *pendingMessageQueue) takeOne() (pendingMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return pendingMessage{}, false
	}
	item := q.items[0]
	q.items[0] = pendingMessage{}
	q.items = q.items[1:]
	q.revision++
	item.message = cloneUserMessage(item.message)
	return item, true
}

func (q *pendingMessageQueue) takeAllForRestore() []pendingMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	items := make([]pendingMessage, len(q.items))
	for i, item := range q.items {
		items[i] = pendingMessage{input: item.input, message: cloneUserMessage(item.message), retryable: item.retryable}
	}
	q.items = nil
	q.revision++
	return items
}

func (q *pendingMessageQueue) restoreFront(message pendingMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	message.message = cloneUserMessage(message.message)
	q.items = append([]pendingMessage{message}, q.items...)
	q.revision++
}

func (q *pendingMessageQueue) revisionValue() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.revision
}

func newPendingMessage(message llm.UserMessage) pendingMessage {
	return pendingMessage{input: llm.TextOf(message.Content), message: cloneUserMessage(message)}
}

func cloneUserMessage(message llm.UserMessage) llm.UserMessage {
	message.Content = append([]llm.Content(nil), message.Content...)
	return message
}

func pendingMessageTexts(items []pendingMessage) []string {
	messages := make([]string, len(items))
	for i, item := range items {
		messages[i] = llm.DisplayText(item.message.Content)
	}
	return messages
}

// QueueKind 区分运行中注入与运行后注入两条队列
type QueueKind uint8

const (
	// QueueSteering 在模型或工具执行间隙注入
	QueueSteering QueueKind = iota
	// QueueFollowUp 在当前运行结束后注入
	QueueFollowUp
)

// ImagePart 是队列消息中的一张图片，Data 为 base64 编码内容
type ImagePart struct {
	Data     string
	MIMEType string
}

// PendingPayload 是入队的一条待注入内容
type PendingPayload struct {
	Text   string
	Images []ImagePart
}

func (p PendingPayload) isEmpty() bool {
	return strings.TrimSpace(p.Text) == "" && len(p.Images) == 0
}

// UserMessage 把待注入内容转成领域消息，空白文本按无文本处理
func (p PendingPayload) UserMessage() llm.UserMessage {
	content := make([]llm.Content, 0, len(p.Images)+1)
	if text := strings.TrimSpace(p.Text); text != "" {
		content = append(content, llm.TextContent{Text: text})
	}
	for _, image := range p.Images {
		content = append(content, llm.ImageContent{Data: image.Data, MIMEType: image.MIMEType})
	}
	return llm.UserMessage{Content: content}
}

// PayloadFromMessage 把领域消息转成待注入内容，保留图片
func PayloadFromMessage(message llm.UserMessage) PendingPayload { return payloadOf(message) }

func payloadOf(message llm.UserMessage) PendingPayload {
	payload := PendingPayload{}
	for _, content := range message.Content {
		switch content := content.(type) {
		case llm.TextContent:
			payload.Text += content.Text
		case llm.ImageContent:
			payload.Images = append(payload.Images, ImagePart{Data: content.Data, MIMEType: content.MIMEType})
		}
	}
	return payload
}

// queue 按种类返回对应队列，是两条队列共用一套 API 的唯一分叉点
func (a *Agent) queue(kind QueueKind) *pendingMessageQueue {
	if kind == QueueFollowUp {
		return &a.followUpQueue
	}
	return &a.steeringQueue
}

// Enqueue 把一条内容加入指定队列
func (a *Agent) Enqueue(kind QueueKind, payload PendingPayload) error {
	if payload.isEmpty() {
		if kind == QueueFollowUp {
			return errors.New("agent: follow-up input is empty")
		}
		return errors.New("agent: steering input is empty")
	}
	a.queue(kind).append(newPendingMessage(payload.UserMessage()))
	return nil
}

// Steer 把一段纯文本加入 steering 队列
func (a *Agent) Steer(input string) error {
	return a.Enqueue(QueueSteering, PendingPayload{Text: input})
}

// SteerMessage 把一条带图片的用户消息加入 steering 队列
func (a *Agent) SteerMessage(message llm.UserMessage) error {
	return a.Enqueue(QueueSteering, PayloadFromMessage(message))
}

// FollowUpMessage 把一条带图片的用户消息加入 follow-up 队列
func (a *Agent) FollowUpMessage(message llm.UserMessage) error {
	return a.Enqueue(QueueFollowUp, PayloadFromMessage(message))
}

// FollowUp 把一段纯文本加入 follow-up 队列
func (a *Agent) FollowUp(input string) error {
	return a.Enqueue(QueueFollowUp, PendingPayload{Text: input})
}

// Len 返回队列中等待消费的条数
func (a *Agent) Len(kind QueueKind) int { return len(a.queue(kind).snapshot()) }

// Revision 返回队列的修订号，供 UI 判断是否需要刷新
func (a *Agent) Revision(kind QueueKind) uint64 { return a.queue(kind).revisionValue() }

// Messages 返回队列内容的纯文本视图
func (a *Agent) Messages(kind QueueKind) []string {
	return pendingMessageTexts(a.queue(kind).snapshot())
}

// Payloads 返回队列内容的完整视图，保留图片
func (a *Agent) Payloads(kind QueueKind) []PendingPayload {
	items := a.queue(kind).snapshot()
	payloads := make([]PendingPayload, len(items))
	for i, item := range items {
		payloads[i] = payloadOf(item.message)
	}
	return payloads
}

// Take 取出并清空队列，按 FIFO 返回
func (a *Agent) Take(kind QueueKind) []PendingPayload {
	items := a.queue(kind).takeAllForRestore()
	payloads := make([]PendingPayload, len(items))
	for i, item := range items {
		payloads[i] = payloadOf(item.message)
	}
	return payloads
}

// Clear 清空队列并返回被清掉的内容
// Restore 把一批内容按顺序放回队列头部，用于撤回未消费的输入
func (a *Agent) Restore(kind QueueKind, payloads []PendingPayload) {
	queue := a.queue(kind)
	for i := len(payloads) - 1; i >= 0; i-- {
		queue.restoreFront(newPendingMessage(payloads[i].UserMessage()))
	}
}

// Acknowledge 按谓词确认一条已由会话补写的内容，只处理可重试的条目
func (a *Agent) Acknowledge(kind QueueKind, match func(PendingPayload) bool) bool {
	return a.acknowledgeQueue(a.queue(kind), func(message pendingMessage) bool {
		return match(payloadOf(message.message))
	})
}

// AcknowledgePendingText 按纯文本确认最近一次 commit 失败恢复的队列项，
// 是 AcknowledgePending 的宽松回退：落盘时消息可能被改写，完整内容对不上
func (a *Agent) AcknowledgePendingText(text string) bool {
	queue := a.pendingQueue()
	if queue == nil {
		return false
	}
	return a.acknowledgeQueue(queue, func(message pendingMessage) bool { return message.input == text })
}

// AcknowledgePending 只确认最近一次 commit 失败恢复的那条队列，
// 同时匹配内容与队列类型，避免相同文本跨队列误删
func (a *Agent) AcknowledgePending(payload PendingPayload) bool {
	queue := a.pendingQueue()
	if queue == nil {
		return false
	}
	return a.acknowledgeQueue(queue, func(message pendingMessage) bool {
		return reflect.DeepEqual(payloadOf(message.message), payload)
	})
}

func (a *Agent) pendingQueue() *pendingMessageQueue {
	a.retryableMu.Lock()
	defer a.retryableMu.Unlock()
	return a.retryableQueue
}

func (a *Agent) markRetryableQueue(queue *pendingMessageQueue) {
	a.retryableMu.Lock()
	a.retryableQueue = queue
	a.retryableMu.Unlock()
}

func (a *Agent) clearRetryableQueue(queue *pendingMessageQueue) {
	a.retryableMu.Lock()
	if a.retryableQueue == queue {
		a.retryableQueue = nil
	}
	a.retryableMu.Unlock()
}

func (a *Agent) acknowledgeQueue(queue *pendingMessageQueue, match func(pendingMessage) bool) bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	for i, message := range queue.items {
		if message.retryable && match(message) {
			queue.items = append(queue.items[:i], queue.items[i+1:]...)
			queue.revision++
			a.clearRetryableQueue(queue)
			return true
		}
	}
	return false
}

func (a *Agent) takeSteering() (pendingMessage, bool) { return a.steeringQueue.takeOne() }

func (a *Agent) restoreSteering(message pendingMessage) { a.steeringQueue.restoreFront(message) }

// PreparedRequest 是一次模型请求的配置与上下文快照；
// Compacted 表示来源结构已重建，运行汇总从新快照的末尾开始
type PreparedRequest struct {
	Context   llm.Context
	Config    llm.AgentConfig
	Compacted bool
	// TransientAck 在本次请求成功提交后确认 transient 附件已被消费
	TransientAck func()
	// ClaimNotifications 认领待投递的子任务通知。返回要落盘的消息与
	// 落盘成功后的 ack 回调;队列为空时返回 nil
	ClaimNotifications func() ([]llm.Message, func())
}

// Options 是一次装配的全部参数，运行期变更走 Set* 与 Reconfigure
type Options struct {
	Config llm.AgentConfig
	// Context 是初始上下文，含工具与消息
	Context llm.Context
	Stream  llm.StreamFn
	Events  llm.AgentEventSink
	// WorkingDir 是工具执行的工作目录
	WorkingDir string
	// MaxTurns 为 0 表示不限制
	MaxTurns int
	// ForkChild 标记该 agent 是 fork 子 agent
	ForkChild bool
	// AgentID 是子 agent 的任务 ID，主 agent 留空
	AgentID string
	Hooks   Hooks
}

// Hooks 是宿主注入的边界回调，核心不持有会话或压缩协调器
type Hooks struct {
	// PrepareRequest 返回宿主准备的请求快照
	PrepareRequest func(context.Context, PreparedRequest) (PreparedRequest, error)
	// CommitMessages 把消息落盘并返回消息 ID
	CommitMessages func([]llm.Message) ([]string, error)
}

// New 按 Options 装配 Agent
func New(opts Options) *Agent {
	return &Agent{
		cfg:        opts.Config,
		ctx:        opts.Context,
		stream:     opts.Stream,
		events:     opts.Events,
		workingDir: opts.WorkingDir,
		maxTurns:   opts.MaxTurns,
		forkChild:  opts.ForkChild,
		agentID:    opts.AgentID,
		hooks:      opts.Hooks,
	}
}

// Reconfigure 替换模型与流函数，保留队列等运行中状态；
// 未提供的字段保持原值，便于只切换模型
// Reconfigure 替换模型配置与流函数，模型切换时使用
func (a *Agent) Reconfigure(config llm.AgentConfig, stream llm.StreamFn) {
	a.cfg = config
	a.stream = stream
}

// Emit 把事件交给当前订阅者，供宿主在装配后触发已安装的处理器
func (a *Agent) Emit(event llm.AgentEvent) error { return a.emit(event) }

// WrapEvents 在现有事件回调外面再包一层，保留已有订阅者；
// 用于子 agent 在装配后追加事件处理
func (a *Agent) WrapEvents(wrap func(next llm.AgentEventSink) llm.AgentEventSink) {
	a.events = wrap(a.events)
}

// SetStream 替换流函数，保留模型与队列
func (a *Agent) SetStream(stream llm.StreamFn) { a.stream = stream }

// SetTools 替换工具集合，工具装配变化时使用
func (a *Agent) SetTools(tools []llm.Tool) { a.ctx.Tools = tools }

// SetThinkingLevel 就地更新思考等级，不重建 agent
func (a *Agent) SetThinkingLevel(level llm.ThinkingLevel) { a.cfg.ThinkingLevel = level }

// SetModel 就地更新模型元数据，流函数由 Reconfigure 负责
func (a *Agent) SetModel(model llm.Model) { a.cfg.Model = model }

// Context 返回当前上下文；工具能力位等就地修改通过 Tools 进行
func (a *Agent) Context() llm.Context { return a.ctx }

func (a *Agent) Config() llm.AgentConfig { return a.cfg }
func (a *Agent) Stream() llm.StreamFn    { return a.stream }
func (a *Agent) WorkingDir() string      { return a.workingDir }
func (a *Agent) MaxTurns() int           { return a.maxTurns }
func (a *Agent) ForkChild() bool         { return a.forkChild }
func (a *Agent) AgentID() string         { return a.agentID }
func (a *Agent) Hooks() Hooks            { return a.hooks }

// acquireRunGuard 独占一次运行的执行权，并发 Run 在此排队且等待期间可被取消
func (a *Agent) acquireRunGuard(ctx context.Context) (func(), error) {
	a.runGuardOnce.Do(func() { a.runGuard = make(chan struct{}, 1) })
	select {
	case a.runGuard <- struct{}{}:
		// select 在守卫就绪与 ctx 取消同时发生时随机选分支，需复查取消
		select {
		case <-ctx.Done():
			<-a.runGuard
			return nil, ctx.Err()
		default:
		}
		return func() { <-a.runGuard }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *Agent) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	return a.RunMessage(ctx, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

func (a *Agent) RunMessage(ctx context.Context, userMessage llm.UserMessage) (*llm.AssistantMessage, error) {
	// 同一 Agent 的多次运行在此排队串行执行
	release, err := a.acquireRunGuard(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.currentUserID = ""
	firstMessageIndex := len(a.ctx.Messages)
	if err := a.emit(llm.AgentStartEvent{}); err != nil {
		return nil, err
	}

	// 1.发出用户消息事件并写入上下文
	if err := a.emit(llm.TurnStartEvent{}); err != nil {
		return nil, err
	}
	if err := a.emit(llm.MessageStartEvent{Message: userMessage}); err != nil {
		return nil, err
	}
	ids, err := a.commitMessagesWithIDs([]llm.Message{userMessage})
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		a.currentUserID = ids[0]
	}
	return a.runLoop(llm.WithCurrentUserMessageID(ctx, a.currentUserID), cancel, firstMessageIndex, nil, nil)
}

func (a *Agent) Continue(ctx context.Context) (*llm.AssistantMessage, error) {
	return a.Resume(ctx, nil)
}

// ContinueSteering 只在成功认领一条 steering 时续跑，避免撤回与操作收尾之间启动空请求
func (a *Agent) ContinueSteering(ctx context.Context) (*llm.AssistantMessage, error) {
	return a.resume(ctx, nil, true)
}

func (a *Agent) Resume(ctx context.Context, committed *llm.AssistantMessage) (*llm.AssistantMessage, error) {
	return a.resume(ctx, committed, false)
}

func (a *Agent) resume(ctx context.Context, committed *llm.AssistantMessage, requireSteering bool) (*llm.AssistantMessage, error) {
	var queue *pendingMessageQueue
	if requireSteering {
		queue = &a.steeringQueue
	}
	return a.resumeQueued(ctx, committed, queue)
}

// ContinueFollowUp 在当前 run 完成后启动一个只消费一条 follow-up 的后继 run
func (a *Agent) ContinueFollowUp(ctx context.Context) (*llm.AssistantMessage, error) {
	return a.resumeQueued(ctx, nil, &a.followUpQueue)
}

func (a *Agent) resumeQueued(ctx context.Context, committed *llm.AssistantMessage, queue *pendingMessageQueue) (*llm.AssistantMessage, error) {
	release, err := a.acquireRunGuard(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if queue == &a.followUpQueue {
		a.currentUserID = ""
	}
	ctx = llm.WithCurrentUserMessageID(ctx, a.currentUserID)
	firstMessageIndex := len(a.ctx.Messages)
	var initialPending *pendingMessage
	if queue != nil {
		pending, ok := queue.takeOne()
		if !ok {
			return nil, nil
		}
		initialPending = &pending
	}
	if err := a.emit(llm.AgentStartEvent{}); err != nil {
		if initialPending != nil {
			queue.restoreFront(*initialPending)
		}
		return nil, err
	}
	if err := a.emit(llm.TurnStartEvent{}); err != nil {
		if initialPending != nil {
			queue.restoreFront(*initialPending)
		}
		return nil, err
	}
	if committed != nil {
		calls := assistantToolCalls(*committed)
		if committed.StopReason == llm.StopReasonAborted || len(calls) == 0 || isRecoverableLength(*committed, a.cfg.Model) {
			if err := a.emit(llm.TurnEndEvent{Message: *committed}); err != nil {
				return nil, err
			}
			if err := a.emitAgentEndWithMessages(firstMessageIndex, []llm.Message{*committed}); err != nil {
				return nil, err
			}
			return committed, nil
		}
		results, err := a.executeToolBatch(ctx, calls)
		if err != nil {
			return nil, err
		}
		if err := a.emit(llm.TurnEndEvent{Message: *committed, ToolResults: results}); err != nil {
			return nil, err
		}
		if err := a.emit(llm.TurnStartEvent{}); err != nil {
			return nil, err
		}
	}
	return a.runLoop(ctx, cancel, firstMessageIndex, initialPending, queue)
}

// ReplaceMessages 用编排层提供的投影消息替换内存消息序列
func (a *Agent) ReplaceMessages(messages []llm.Message) {
	a.ctx.Messages = messages
}

func (a *Agent) AppendMessage(msg llm.Message) {
	a.ctx.Messages = append(a.ctx.Messages, msg)
}

// runLoop 消费模型流并执行工具，循环顶回调编排层做阈值压缩
func (a *Agent) runLoop(ctx context.Context, cancel context.CancelFunc, firstMessageIndex int, initialPending *pendingMessage, initialQueue *pendingMessageQueue) (*llm.AssistantMessage, error) {
	turns := 0
	for {
		turns++
		// 1.认领本轮首条用户消息
		pending, pendingQueue, ok := a.claimPending(&initialPending, &initialQueue)
		if ok {
			var err error
			ctx, err = a.admitPending(ctx, pending, pendingQueue)
			if err != nil {
				return nil, err
			}
		}

		// 2.请求准备只返回快照，摘要失败的降级与存储失败的停止由宿主判断
		var acknowledgeTransient func()
		var claimNotifications func() ([]llm.Message, func())
		request, err := a.prepareRequest(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return a.finishModelFailure(firstMessageIndex, llm.StopReasonAborted, err, nil)
			}
			return nil, err
		}
		if request != nil {
			a.ctx, a.cfg = request.Context, request.Config
			acknowledgeTransient = request.TransientAck
			claimNotifications = request.ClaimNotifications
			if request.Compacted {
				firstMessageIndex = len(a.ctx.Messages)
			}
		}

		// 3.把待投递的子任务通知落盘进主会话,模型后续轮次仍能回看;
		// 落盘成功才 ack,失败时通知留在队列等下次唤醒
		// 上下文追加由 commitMessages 统一负责,这里不能重复 append
		if claimNotifications != nil {
			messages, ack := claimNotifications()
			if len(messages) > 0 {
				if err := a.commitMessages(messages); err != nil {
					cancel()
					return nil, err
				}
				if ack != nil {
					ack()
				}
			}
		}

		// 4.消费模型流并同步发出 assistant partial 更新
		final, partial, streamReason, streamErr, err := a.consumeStream(ctx)
		if err != nil {
			return nil, err
		}
		if streamErr != nil || streamReason == llm.StopReasonAborted {
			reason := streamReason
			if reason != llm.StopReasonAborted {
				reason = llm.StopReasonError
			}
			return a.finishModelFailure(firstMessageIndex, reason, streamErr, &partial)
		}

		// 5.提交最终 assistant 消息，元数据在提交时补全
		committed, err := a.commitTurn(*final, acknowledgeTransient)
		if err != nil {
			cancel()
			return nil, err
		}
		final = &committed

		// 5.决定本轮如何收尾：轮次上限、截断恢复、无工具调用、执行工具
		decision := a.decideTurn(*final, turns)
		switch decision {
		case turnMaxReached:
			if err := a.endTurn(firstMessageIndex, *final, nil); err != nil {
				cancel()
				return nil, err
			}
			if len(assistantToolCalls(*final)) > 0 {
				return final, ErrMaxTurns
			}
			return final, nil
		case turnTruncated:
			if err := a.endTurn(firstMessageIndex, *final, nil); err != nil {
				cancel()
				return nil, err
			}
			return final, nil
		case turnFinished:
			if err := a.emit(llm.TurnEndEvent{Message: *final}); err != nil {
				cancel()
				return nil, err
			}
			// 还有排队的 steering 就接着跑下一轮
			next, ok := a.takeSteering()
			if !ok {
				if err := a.emitAgentEnd(firstMessageIndex); err != nil {
					cancel()
					return nil, err
				}
				return final, nil
			}
			if err := a.emit(llm.TurnStartEvent{}); err != nil {
				a.restoreSteering(next)
				cancel()
				return nil, err
			}
			initialPending, initialQueue = &next, &a.steeringQueue
			continue
		}

		// 6.执行工具批次，结束当前 turn 后开始下一轮模型调用
		toolCalls := assistantToolCalls(*final)
		toolResults, err := a.executeToolBatch(ctx, toolCalls)
		if err != nil {
			cancel()
			return nil, err
		}
		if err := a.endTurn(firstMessageIndex, *final, toolResults); err != nil {
			cancel()
			return nil, err
		}
		if err := a.emit(llm.TurnStartEvent{}); err != nil {
			cancel()
			return nil, err
		}
	}
}

// turnDecision 表示一轮结束后主循环该走哪条路
type turnDecision int

const (
	// turnContinue 表示还有工具要执行
	turnContinue turnDecision = iota
	// turnFinished 表示本轮正常结束
	turnFinished
	// turnTruncated 表示输出被截断，交给编排层做溢出恢复
	turnTruncated
	// turnMaxReached 表示达到轮次上限
	turnMaxReached
)

// claimPending 取本轮首条用户消息，调用方指定队列时优先用它
func (a *Agent) claimPending(initialPending **pendingMessage, initialQueue **pendingMessageQueue) (pendingMessage, *pendingMessageQueue, bool) {
	if *initialPending != nil {
		pending, queue := **initialPending, *initialQueue
		*initialPending, *initialQueue = nil, nil
		return pending, queue, true
	}
	pending, ok := a.takeSteering()
	return pending, &a.steeringQueue, ok
}

// admitPending 落盘并广播一条待注入的用户消息，返回带消息 ID 的 ctx
func (a *Agent) admitPending(ctx context.Context, pending pendingMessage, pendingQueue *pendingMessageQueue) (context.Context, error) {
	userMessage := pending.message
	if len(userMessage.Content) == 0 {
		userMessage = llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: pending.input}}}
	}
	ids, err := a.persistMessages([]llm.Message{userMessage})
	if err != nil {
		pending.retryable = true
		pendingQueue.restoreFront(pending)
		a.markRetryableQueue(pendingQueue)
		return ctx, err
	}
	if err := a.emit(llm.MessageStartEvent{Message: userMessage}); err != nil {
		return ctx, err
	}
	var sourceID string
	if len(ids) > 0 {
		sourceID = ids[0]
		a.currentUserID = sourceID
		ctx = llm.WithCurrentUserMessageID(ctx, sourceID)
	} else {
		a.currentUserID = ""
		ctx = llm.WithCurrentUserMessageID(ctx, "")
	}
	if err := a.emit(llm.MessageEndEvent{Message: userMessage, SourceID: sourceID}); err != nil {
		return ctx, err
	}
	return ctx, nil
}

// prepareRequest 让宿主对齐上下文；未挂 hook 时返回 nil
func (a *Agent) prepareRequest(ctx context.Context) (*PreparedRequest, error) {
	if a.hooks.PrepareRequest == nil {
		return nil, nil
	}
	request, err := a.hooks.PrepareRequest(ctx, PreparedRequest{Context: a.ctx, Config: a.cfg})
	if err != nil {
		return nil, err
	}
	return &request, nil
}

// consumeStream 读完整条模型流，返回最终消息、最后一个 partial 与流错误
func (a *Agent) consumeStream(ctx context.Context) (*llm.AssistantMessage, llm.AssistantMessage, llm.StopReason, error, error) {
	partial := a.newAssistantMessage()
	if err := a.emit(llm.MessageStartEvent{Message: partial}); err != nil {
		return nil, partial, llm.StopReasonError, nil, err
	}
	// 每轮单独派生 stream ctx，错误事件分支只取消它
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	requestContext := contextForModel(a.ctx)
	events, streamErr := a.stream(streamCtx, a.cfg.Model, requestContext, llm.Options{
		ThinkingLevel: a.cfg.ThinkingLevel,
		SessionID:     a.cfg.SessionID,
	})

	var final *llm.AssistantMessage
	var streamReason llm.StopReason
	var eventErr error
	toolCallArguments := make(map[int]string)
	if streamErr == nil {
		for event := range events {
			// 消费者失败或模型已终止时仍排空通道，等待生产者释放连接
			if eventErr != nil || streamErr != nil {
				continue
			}
			switch ev := event.(type) {
			case llm.AssistantMessageStartEvent:
				if ev.Partial != nil {
					partial = cloneAssistantMessage(*ev.Partial)
				}
			case llm.AssistantMessageTextStartEvent, llm.AssistantMessageTextDeltaEvent, llm.AssistantMessageTextEndEvent,
				llm.AssistantMessageThinkingStartEvent, llm.AssistantMessageThinkingDeltaEvent, llm.AssistantMessageThinkingEndEvent,
				llm.AssistantMessageToolCallStartEvent, llm.AssistantMessageToolCallDeltaEvent, llm.AssistantMessageToolCallEndEvent:
				if snapshot := assistantEventPartial(ev); snapshot != nil {
					partial = cloneAssistantMessage(*snapshot)
				} else {
					appendAssistantEvent(&partial, ev, toolCallArguments)
				}
				if err := a.emit(llm.MessageUpdateEvent{
					Message:               cloneAssistantMessage(partial),
					AssistantMessageEvent: ev,
				}); err != nil {
					eventErr = err
					streamCancel()
				}
			case llm.AssistantMessageDoneEvent:
				final = ev.Message
			case llm.AssistantMessageErrorEvent:
				streamErr = ev.Err
				if streamErr == nil {
					streamErr = errors.New("agent: model stream returned empty error")
				}
				streamReason = ev.Reason
				if streamReason != llm.StopReasonAborted && streamReason != llm.StopReasonError {
					streamReason = llm.StopReasonError
				}
				if ev.Partial != nil {
					partial = cloneAssistantMessage(*ev.Partial)
				}
				streamCancel()
			}
		}
	}
	streamCause := streamCtx.Err()
	streamCancel()
	if streamErr != nil && ctx.Err() != nil {
		streamReason = llm.StopReasonAborted
	}
	if eventErr != nil {
		return nil, partial, streamReason, nil, eventErr
	}
	if streamErr == nil && final == nil {
		if errors.Is(streamCause, context.Canceled) || errors.Is(streamCause, context.DeadlineExceeded) {
			streamReason = llm.StopReasonAborted
		} else {
			streamErr = errors.New("agent: stream ended without a final message")
		}
	}
	return final, partial, streamReason, streamErr, nil
}

// commitTurn 提交最终 assistant 消息并结算费用，返回补全元数据后的消息
func (a *Agent) commitTurn(final llm.AssistantMessage, acknowledgeTransient func()) (llm.AssistantMessage, error) {
	setAssistantMetadata(&final, a.cfg.Model)
	if final.Usage.Cost == nil {
		final.Usage.Cost = llm.PriceUsage(a.cfg.Model, final.Usage)
	}
	if err := a.commitMessages([]llm.Message{final}); err != nil {
		return final, err
	}
	if acknowledgeTransient != nil {
		acknowledgeTransient()
	}
	return final, nil
}

// decideTurn 判断本轮结束后主循环的去向
func (a *Agent) decideTurn(final llm.AssistantMessage, turns int) turnDecision {
	if a.maxTurns > 0 && turns >= a.maxTurns {
		return turnMaxReached
	}
	// 输出在达到模型上限前被截断且启用了编排层：跳过截断消息里的工具调用，
	// 正常提交本轮后由编排层驱动溢出恢复；达到上限的截断是正常输出限制
	if a.hooks.PrepareRequest != nil && isRecoverableLength(final, a.cfg.Model) {
		return turnTruncated
	}
	if len(assistantToolCalls(final)) == 0 {
		return turnFinished
	}
	return turnContinue
}

// endTurn 广播 turn 结束并开启下一轮
func (a *Agent) endTurn(firstMessageIndex int, final llm.AssistantMessage, toolResults []llm.ToolResultMessage) error {
	if toolResults == nil {
		if err := a.emit(llm.TurnEndEvent{Message: final}); err != nil {
			return err
		}
		return a.emitAgentEnd(firstMessageIndex)
	}
	return a.emit(llm.TurnEndEvent{Message: final, ToolResults: toolResults})
}

// contextForModel 过滤已错误收尾的 assistant，避免取消/失败文本污染下一次请求
func contextForModel(agentContext llm.Context) llm.Context {
	messages := make([]llm.Message, 0, len(agentContext.Messages))
	for _, message := range agentContext.Messages {
		if assistant, ok := message.(llm.AssistantMessage); ok && (assistant.StopReason == llm.StopReasonError || assistant.StopReason == llm.StopReasonAborted) {
			continue
		}
		messages = append(messages, message)
	}
	agentContext.Messages = messages
	return agentContext
}

// isRecoverableLength 输出在达到模型输出上限之前被截断，说明截断来自上下文耗尽而非输出上限
func isRecoverableLength(message llm.AssistantMessage, model llm.Model) bool {
	if message.StopReason != llm.StopReasonLength || model.MaxTokens <= 0 {
		return false
	}
	return message.Usage.OutputTokens < model.MaxTokens
}

func cloneAssistantMessage(message llm.AssistantMessage) llm.AssistantMessage {
	message.Content = append([]llm.Content(nil), message.Content...)
	return message
}

func (a *Agent) newAssistantMessage() llm.AssistantMessage {
	message := llm.AssistantMessage{}
	setAssistantMetadata(&message, a.cfg.Model)
	return message
}

func setAssistantMetadata(message *llm.AssistantMessage, model llm.Model) {
	message.API = model.API
	message.Provider = model.Provider
	message.Model = model.ID
}

func appendAssistantEvent(message *llm.AssistantMessage, event llm.AssistantMessageEvent, toolCallArguments map[int]string) {
	var index int
	switch event := event.(type) {
	case llm.AssistantMessageTextStartEvent:
		index = event.ContentIndex
		setAssistantContent(message, index, llm.TextContent{})
	case llm.AssistantMessageTextDeltaEvent:
		index = event.ContentIndex
		if ensureAssistantContentSlot(message, index) {
			// slot 不是文本时保持原类型，不做静默覆写
			llm.AppendText(message, index, event.Delta)
		}
	case llm.AssistantMessageTextEndEvent:
		index = event.ContentIndex
		setAssistantContent(message, index, llm.TextContent{Text: event.Content})
	case llm.AssistantMessageThinkingStartEvent:
		index = event.ContentIndex
		setAssistantContent(message, index, llm.ThinkingContent{})
	case llm.AssistantMessageThinkingDeltaEvent:
		index = event.ContentIndex
		if ensureAssistantContentSlot(message, index) {
			// slot 不是思考时保持原类型，不做静默覆写
			llm.AppendThinking(message, index, event.Delta)
		}
	case llm.AssistantMessageThinkingEndEvent:
		index = event.ContentIndex
		setAssistantContent(message, index, llm.ThinkingContent{Thinking: event.Content})
	case llm.AssistantMessageToolCallStartEvent:
		index = event.ContentIndex
		setAssistantContent(message, index, event.ToolCall)
	case llm.AssistantMessageToolCallDeltaEvent:
		index = event.ContentIndex
		toolCallArguments[index] += event.Delta
		var arguments map[string]any
		// delta 携带的是不完整的 JSON 片段，能反序列化成功才说明工具参数收全
		if json.Unmarshal([]byte(toolCallArguments[index]), &arguments) == nil && ensureAssistantContentSlot(message, index) {
			call, ok := message.Content[index].(llm.ToolCall)
			if !ok {
				call = llm.ToolCall{}
			}
			call.Arguments = arguments
			message.Content[index] = call
		}
	case llm.AssistantMessageToolCallEndEvent:
		index = event.ContentIndex
		setAssistantContent(message, index, event.ToolCall)
	}
}

func assistantEventPartial(event llm.AssistantMessageEvent) *llm.AssistantMessage {
	switch event := event.(type) {
	case llm.AssistantMessageTextStartEvent:
		return event.Partial
	case llm.AssistantMessageTextDeltaEvent:
		return event.Partial
	case llm.AssistantMessageTextEndEvent:
		return event.Partial
	case llm.AssistantMessageThinkingStartEvent:
		return event.Partial
	case llm.AssistantMessageThinkingDeltaEvent:
		return event.Partial
	case llm.AssistantMessageThinkingEndEvent:
		return event.Partial
	case llm.AssistantMessageToolCallStartEvent:
		return event.Partial
	case llm.AssistantMessageToolCallDeltaEvent:
		return event.Partial
	case llm.AssistantMessageToolCallEndEvent:
		return event.Partial
	}
	return nil
}

func setAssistantContent(message *llm.AssistantMessage, index int, content llm.Content) bool {
	if !ensureAssistantContentSlot(message, index) {
		return false
	}
	message.Content[index] = content
	return true
}

func ensureAssistantContentSlot(message *llm.AssistantMessage, index int) bool {
	if index < 0 {
		return false
	}
	for len(message.Content) <= index {
		message.Content = append(message.Content, nil)
	}
	return true
}

// finishModelFailure 将模型流终止记录为 assistant 消息并结束本次运行，
// aborted 保留已收到的 partial 内容，error 记录错误文本
func (a *Agent) finishModelFailure(firstMessageIndex int, reason llm.StopReason, err error, partial *llm.AssistantMessage) (*llm.AssistantMessage, error) {
	message := a.newAssistantMessage()
	message.StopReason = reason
	if partial != nil {
		message.Usage = partial.Usage
	}
	if message.Usage.Cost == nil {
		message.Usage.Cost = llm.PriceUsage(a.cfg.Model, message.Usage)
	}
	if reason == llm.StopReasonAborted {
		// 取消属于用户操作而非故障：固定文案，保留已收到的 partial 内容
		message.ErrorMessage = "Operation aborted"
		if partial != nil && len(partial.Content) > 0 {
			message.Content = append([]llm.Content(nil), partial.Content...)
		} else {
			message.Content = []llm.Content{llm.TextContent{Text: ""}}
		}
	} else {
		message.ErrorMessage = err.Error()
		message.Content = []llm.Content{llm.TextContent{Text: err.Error()}}
	}
	if emitErr := a.commitMessages([]llm.Message{message}); emitErr != nil {
		return nil, emitErr
	}
	if emitErr := a.emitAgentEnd(firstMessageIndex, err); emitErr != nil {
		return nil, emitErr
	}
	if reason == llm.StopReasonAborted {
		return &message, nil
	}
	return &message, err
}

func (a *Agent) emitAgentEnd(firstMessageIndex int, cause ...error) error {
	messages := append([]llm.Message(nil), a.ctx.Messages[firstMessageIndex:]...)
	return a.emitAgentEndMessages(messages, cause...)
}

// emitAgentEndWithMessages 在恢复路径补充已提交但不属于当前内存切片的消息
func (a *Agent) emitAgentEndWithMessages(firstMessageIndex int, extra []llm.Message, cause ...error) error {
	messages := append([]llm.Message(nil), a.ctx.Messages[firstMessageIndex:]...)
	messages = append(messages, extra...)
	return a.emitAgentEndMessages(messages, cause...)
}

func (a *Agent) emitAgentEndMessages(messages []llm.Message, cause ...error) error {
	var err error
	if len(cause) > 0 {
		err = cause[0]
	}
	return a.emit(llm.AgentEndEvent{Messages: messages, Err: err})
}

func (a *Agent) emit(event llm.AgentEvent) error {
	if a.events == nil {
		return nil
	}
	if err := a.events(event); err != nil {
		return fmt.Errorf("agent: event consumer failed: %w", err)
	}
	return nil
}

func (a *Agent) findTool(name string) llm.Tool {
	for _, t := range a.ctx.Tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func (a *Agent) execute(ctx context.Context, name string, args map[string]any) (llm.ToolResult, error) {
	t := a.findTool(name)
	if t == nil {
		return llm.ToolResult{}, fmt.Errorf("agent: no tool named %q", name)
	}
	return t.Execute(ctx, args)
}

func (a *Agent) executeToolBatch(ctx context.Context, calls []llm.ToolCall) ([]llm.ToolResultMessage, error) {
	sequential := false
	for _, call := range calls {
		if tool := a.findTool(call.Name); tool != nil && tool.ExecutionMode() == llm.ToolExecutionSequential {
			sequential = true
			break
		}
	}
	if sequential {
		return a.executeSequentialBatch(ctx, calls)
	}
	return a.executeParallelBatch(ctx, calls)
}

func (a *Agent) executeSequentialBatch(ctx context.Context, calls []llm.ToolCall) ([]llm.ToolResultMessage, error) {
	results := make([]llm.ToolResultMessage, 0, len(calls))
	var eventErr error
	completed := 0
	for completed < len(calls) {
		call := calls[completed]
		if eventErr = a.emitToolStart(call); eventErr != nil {
			break
		}
		var result llm.ToolResult
		var isError bool
		if ctx.Err() != nil {
			result, isError = abortedToolResult(), true
		} else {
			result, isError = executeToolCall(ctx, a, call)
		}
		results = append(results, toolResultMessage(call, result, isError))
		completed++
		if eventErr = a.emitToolEnd(call, result, isError); eventErr != nil {
			break
		}
		if isError && ctx.Err() != nil {
			break
		}
	}
	if completed < len(calls) {
		// 中断后未启动的调用也要写入结果，保持 assistant tool call 与 result 一一对应
		for _, call := range calls[completed:] {
			results = append(results, toolResultMessage(call, abortedToolResult(), true))
		}
	}
	if err := a.commitToolResults(results, eventErr == nil); err != nil {
		return results, err
	}
	return results, eventErr
}

type toolCompletion struct {
	index   int
	result  llm.ToolResult
	isError bool
}

func (a *Agent) executeParallelBatch(ctx context.Context, calls []llm.ToolCall) ([]llm.ToolResultMessage, error) {
	for _, call := range calls {
		if err := a.emitToolStart(call); err != nil {
			return nil, err
		}
	}

	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := make(chan toolCompletion, len(calls))
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(index int, toolCall llm.ToolCall) {
			defer wg.Done()
			var result llm.ToolResult
			var isError bool
			if batchCtx.Err() != nil {
				// 已取消：工具不再启动，以 aborted 结果收场
				result, isError = abortedToolResult(), true
			} else {
				result, isError = executeToolCall(batchCtx, a, toolCall)
			}
			completed <- toolCompletion{index: index, result: result, isError: isError}
		}(i, call)
	}

	results := make([]toolCompletion, len(calls))
	var eventErr error
	for range calls {
		completion := <-completed
		results[completion.index] = completion
		if eventErr == nil {
			if err := a.emitToolEnd(calls[completion.index], completion.result, completion.isError); err != nil {
				eventErr = err
				cancel()
			}
		}
	}

	wg.Wait()
	messages := make([]llm.ToolResultMessage, len(calls))
	for i, call := range calls {
		messages[i] = toolResultMessage(call, results[i].result, results[i].isError)
	}
	if err := a.commitToolResults(messages, eventErr == nil); err != nil {
		return messages, err
	}
	return messages, eventErr
}

func executeToolCall(ctx context.Context, a *Agent, call llm.ToolCall) (llm.ToolResult, bool) {
	// 工具需要执行中 agent 的上下文来派发子任务,但不该看到 Agent 本身
	ctx = llm.WithToolContext(ctx, llm.ToolContext{Context: a.ctx, Config: a.cfg, ForkChild: a.forkChild, AgentID: a.agentID})
	ctx = llm.WithWorkingDir(ctx, a.workingDir)
	result, err := a.execute(ctx, call.Name, call.Arguments)
	if err != nil {
		if ctx.Err() != nil {
			result.Content = []llm.Content{llm.TextContent{Text: "Operation aborted"}}
		} else {
			result.Content = []llm.Content{llm.TextContent{Text: "tool error: " + err.Error()}}
		}
		result.IsError = true
	}
	return result, result.IsError
}

func abortedToolResult() llm.ToolResult {
	return llm.ToolResult{
		Content: []llm.Content{llm.TextContent{Text: "Operation aborted"}},
		IsError: true,
	}
}

func (a *Agent) emitToolStart(call llm.ToolCall) error {
	return a.emit(llm.ToolExecutionStartEvent{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Arguments:  call.Arguments,
	})
}

func (a *Agent) emitToolEnd(call llm.ToolCall, result llm.ToolResult, isError bool) error {
	return a.emit(llm.ToolExecutionEndEvent{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Result:     result,
		IsError:    isError,
	})
}

// toolResultMessage 保留已执行工具的完整结果，提交失败也不重新执行工具
func toolResultMessage(call llm.ToolCall, result llm.ToolResult, isError bool) llm.ToolResultMessage {
	return llm.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: result.Content, Details: result.Details, IsError: isError, Usage: result.Usage}
}

func (a *Agent) commitToolResults(results []llm.ToolResultMessage, notify bool) error {
	messages := make([]llm.Message, len(results))
	for i, result := range results {
		messages[i] = result
	}
	var eventErr error
	if notify {
		for _, message := range messages {
			if err := a.emit(llm.MessageStartEvent{Message: message}); err != nil {
				eventErr = err
				notify = false
				break
			}
		}
	}
	return errors.Join(a.commitMessagesWithNotify(messages, notify), eventErr)
}

// commitMessages 先提交宿主记录，再发布核心上下文和结束通知
func (a *Agent) commitMessages(messages []llm.Message) error {
	_, err := a.commitMessagesWithIDs(messages)
	return err
}

func (a *Agent) commitMessagesWithNotify(messages []llm.Message, notify bool) error {
	_, err := a.commitMessagesWithNotifyIDs(messages, notify)
	return err
}

func (a *Agent) commitMessagesWithIDs(messages []llm.Message) ([]string, error) {
	return a.commitMessagesWithNotifyIDs(messages, true)
}

func (a *Agent) commitMessagesWithNotifyIDs(messages []llm.Message, notify bool) ([]string, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	ids, err := a.persistMessages(messages)
	if err != nil {
		if notify {
			for i, message := range messages {
				if i < len(ids) && ids[i] != "" {
					if eventErr := a.emit(llm.MessageEndEvent{Message: message, SourceID: ids[i], Pending: true}); eventErr != nil {
						return ids, errors.Join(err, eventErr)
					}
				}
			}
		}
		return ids, err
	}
	if !notify {
		return ids, nil
	}
	for i, message := range messages {
		var id string
		if len(ids) > i {
			id = ids[i]
		}
		if err := a.emit(llm.MessageEndEvent{Message: message, SourceID: id}); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

func (a *Agent) persistMessages(messages []llm.Message) ([]string, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	var ids []string
	if a.hooks.CommitMessages != nil {
		var err error
		ids, err = a.hooks.CommitMessages(messages)
		if err != nil {
			return ids, err
		}
	}
	a.ctx.Messages = append(a.ctx.Messages, messages...)
	return ids, nil
}

func assistantToolCalls(message llm.AssistantMessage) []llm.ToolCall {
	var calls []llm.ToolCall
	for _, content := range message.Content {
		if call, ok := content.(llm.ToolCall); ok {
			calls = append(calls, call)
		}
	}
	return calls
}
