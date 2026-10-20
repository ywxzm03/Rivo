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
	AgentConfig  llm.AgentConfig
	AgentContext llm.Context
	StreamFn     llm.StreamFn
	OnEvent      llm.AgentEventSink
	// PrepareRequest 返回宿主准备的请求快照，核心不持有会话或压缩协调器
	PrepareRequest func(context.Context, PreparedRequest) (PreparedRequest, error)
	CommitMessages func([]llm.Message) ([]string, error)

	runGuardOnce     sync.Once
	runGuard         chan struct{}
	steeringMu       sync.Mutex
	steering         []steeringMessage
	steeringRevision uint64
	currentUserID    string
}

type steeringMessage struct {
	input     string
	message   llm.UserMessage
	retryable bool
}

func cloneUserMessage(message llm.UserMessage) llm.UserMessage {
	message.Content = append([]llm.Content(nil), message.Content...)
	return message
}

func textOfUserMessage(message llm.UserMessage) string {
	var parts []string
	for _, content := range message.Content {
		if text, ok := content.(llm.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func displayUserMessageText(message llm.UserMessage) string {
	var parts []string
	for _, content := range message.Content {
		switch content := content.(type) {
		case llm.TextContent:
			parts = append(parts, content.Text)
		case llm.ImageContent:
			label := "[image]"
			if content.MIMEType != "" {
				label = "[image: " + content.MIMEType + "]"
			}
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, "")
}

func equalUserMessage(left, right llm.UserMessage) bool {
	return reflect.DeepEqual(left, right)
}

// Steer 把用户输入加入当前运行的 steering 队列，不打断正在进行的模型或工具
func (a *Agent) Steer(input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return errors.New("agent: steering input is empty")
	}
	return a.SteerMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

func (a *Agent) SteerMessage(message llm.UserMessage) error {
	if len(message.Content) == 0 {
		return errors.New("agent: steering input is empty")
	}
	a.steeringMu.Lock()
	a.steering = append(a.steering, steeringMessage{input: textOfUserMessage(message), message: cloneUserMessage(message)})
	a.steeringRevision++
	a.steeringMu.Unlock()
	return nil
}

func (a *Agent) HasSteering() bool {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	return len(a.steering) > 0
}

func (a *Agent) SteeringMessages() []string {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	messages := make([]string, len(a.steering))
	for i, message := range a.steering {
		messages[i] = displayUserMessageText(message.message)
	}
	return messages
}

func (a *Agent) SteeringUserMessages() []llm.UserMessage {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	messages := make([]llm.UserMessage, len(a.steering))
	for i, message := range a.steering {
		messages[i] = cloneUserMessage(message.message)
	}
	return messages
}

func (a *Agent) SteeringRevision() uint64 {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	return a.steeringRevision
}

// TakeSteering 清空等待消费的 steering，按 FIFO 返回消息文本
func (a *Agent) TakeSteering() []string {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	messages := make([]string, len(a.steering))
	for i, message := range a.steering {
		messages[i] = message.input
	}
	if len(a.steering) > 0 {
		a.steering = nil
		a.steeringRevision++
	}
	return messages
}

func (a *Agent) TakeSteeringMessages() []llm.UserMessage {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	messages := make([]llm.UserMessage, len(a.steering))
	for i, message := range a.steering {
		messages[i] = cloneUserMessage(message.message)
	}
	if len(a.steering) > 0 {
		a.steering = nil
		a.steeringRevision++
	}
	return messages
}

// AcknowledgeSteering 确认宿主提交失败后恢复、且已由会话补写的 steering
func (a *Agent) AcknowledgeSteering(input string) bool {
	return a.acknowledgeSteering(func(message steeringMessage) bool { return message.input == input })
}

func (a *Agent) AcknowledgeSteeringMessage(input llm.UserMessage) bool {
	return a.acknowledgeSteering(func(message steeringMessage) bool { return equalUserMessage(message.message, input) })
}

func (a *Agent) acknowledgeSteering(match func(steeringMessage) bool) bool {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	for i, message := range a.steering {
		if message.retryable && match(message) {
			a.steering = append(a.steering[:i], a.steering[i+1:]...)
			a.steeringRevision++
			return true
		}
	}
	return false
}

func (a *Agent) takeSteering() (steeringMessage, bool) {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	if len(a.steering) == 0 {
		return steeringMessage{}, false
	}
	message := a.steering[0]
	a.steering = a.steering[1:]
	a.steeringRevision++
	return message, true
}

func (a *Agent) restoreSteering(message steeringMessage) {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	a.steering = append([]steeringMessage{message}, a.steering...)
	a.steeringRevision++
}

// PreparedRequest 是一次模型请求的配置与上下文快照；
// Compacted 表示来源结构已重建，运行汇总从新快照的末尾开始
type PreparedRequest struct {
	Context   llm.Context
	Config    llm.AgentConfig
	Compacted bool
}

func NewAgent(cfg llm.AgentConfig, agentCtx llm.Context, streamFn llm.StreamFn, onEvent llm.AgentEventSink) *Agent {
	return &Agent{
		AgentConfig:  cfg,
		AgentContext: agentCtx,
		StreamFn:     streamFn,
		OnEvent:      onEvent,
	}
}

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
	firstMessageIndex := len(a.AgentContext.Messages)
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
	return a.runLoop(llm.WithCurrentUserMessageID(ctx, a.currentUserID), cancel, firstMessageIndex, nil)
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
	release, err := a.acquireRunGuard(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = llm.WithCurrentUserMessageID(ctx, a.currentUserID)
	firstMessageIndex := len(a.AgentContext.Messages)
	var initialSteering *steeringMessage
	if requireSteering {
		pending, ok := a.takeSteering()
		if !ok {
			return nil, nil
		}
		initialSteering = &pending
	}
	if err := a.emit(llm.AgentStartEvent{}); err != nil {
		if initialSteering != nil {
			a.restoreSteering(*initialSteering)
		}
		return nil, err
	}
	if err := a.emit(llm.TurnStartEvent{}); err != nil {
		if initialSteering != nil {
			a.restoreSteering(*initialSteering)
		}
		return nil, err
	}
	if committed != nil {
		calls := assistantToolCalls(*committed)
		if committed.StopReason == llm.StopReasonAborted || len(calls) == 0 || isRecoverableLength(*committed, a.AgentConfig.Model) {
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
	return a.runLoop(ctx, cancel, firstMessageIndex, initialSteering)
}

// ReplaceMessages 用编排层提供的投影消息替换内存消息序列
func (a *Agent) ReplaceMessages(messages []llm.Message) {
	a.AgentContext.Messages = messages
}

func (a *Agent) AppendMessage(msg llm.Message) {
	a.AgentContext.Messages = append(a.AgentContext.Messages, msg)
}

// runLoop 消费模型流并执行工具，循环顶回调编排层做阈值压缩
func (a *Agent) runLoop(ctx context.Context, cancel context.CancelFunc, firstMessageIndex int, initialSteering *steeringMessage) (*llm.AssistantMessage, error) {
	for {
		// 1.先原子认领一条 steering，撤回只能取得尚未认领的消息
		var pending steeringMessage
		var ok bool
		if initialSteering != nil {
			pending, ok = *initialSteering, true
			initialSteering = nil
		} else {
			pending, ok = a.takeSteering()
		}
		if ok {
			userMessage := pending.message
			if len(userMessage.Content) == 0 {
				userMessage = llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: pending.input}}}
			}
			ids, err := a.persistMessages([]llm.Message{userMessage})
			if err != nil {
				pending.retryable = true
				a.restoreSteering(pending)
				return nil, err
			}
			if err := a.emit(llm.MessageStartEvent{Message: userMessage}); err != nil {
				return nil, err
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
				return nil, err
			}
		}

		// 2.请求准备只返回快照，摘要失败的降级与存储失败的停止由宿主判断
		if a.PrepareRequest != nil {
			request, err := a.PrepareRequest(ctx, PreparedRequest{Context: a.AgentContext, Config: a.AgentConfig})
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return a.finishModelFailure(firstMessageIndex, llm.StopReasonAborted, err, nil)
				}
				return nil, err
			}
			a.AgentContext, a.AgentConfig = request.Context, request.Config
			if request.Compacted {
				firstMessageIndex = len(a.AgentContext.Messages)
			}
		}
		// 3.消费模型流并同步发出 assistant partial 更新
		partial := a.newAssistantMessage()
		if err := a.emit(llm.MessageStartEvent{Message: partial}); err != nil {
			return nil, err
		}
		// 每轮单独派生 stream ctx，错误事件分支只取消它
		streamCtx, streamCancel := context.WithCancel(ctx)
		requestContext := contextForModel(a.AgentContext)
		events, streamErr := a.StreamFn(streamCtx, a.AgentConfig.Model, requestContext, llm.Options{
			ThinkingLevel: a.AgentConfig.ThinkingLevel,
			SessionID:     a.AgentConfig.SessionID,
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
			return nil, eventErr
		}
		if streamErr == nil && final == nil {
			if errors.Is(streamCause, context.Canceled) || errors.Is(streamCause, context.DeadlineExceeded) {
				streamReason = llm.StopReasonAborted
			} else {
				streamErr = errors.New("agent: stream ended without a final message")
			}
		}
		if streamErr != nil || streamReason == llm.StopReasonAborted {
			reason := streamReason
			if reason != llm.StopReasonAborted {
				reason = llm.StopReasonError
			}
			return a.finishModelFailure(firstMessageIndex, reason, streamErr, &partial)
		}
		// 4.提交最终 assistant 消息，并检查是否需要执行工具
		setAssistantMetadata(final, a.AgentConfig.Model)
		if final.Usage.Cost == nil {
			final.Usage.Cost = llm.PriceUsage(a.AgentConfig.Model, final.Usage)
		}
		if err := a.commitMessages([]llm.Message{*final}); err != nil {
			cancel()
			return nil, err
		}

		toolCalls := assistantToolCalls(*final)
		// 5.输出在达到模型上限前被截断且启用了编排层：跳过截断消息里的工具调用，
		// 正常提交本轮后由编排层驱动溢出恢复；达到上限的截断是正常输出限制
		if a.PrepareRequest != nil && isRecoverableLength(*final, a.AgentConfig.Model) {
			if err := a.emit(llm.TurnEndEvent{Message: *final}); err != nil {
				cancel()
				return nil, err
			}
			if err := a.emitAgentEnd(firstMessageIndex); err != nil {
				cancel()
				return nil, err
			}
			return final, nil
		}
		if len(toolCalls) == 0 {
			if err := a.emit(llm.TurnEndEvent{Message: *final}); err != nil {
				cancel()
				return nil, err
			}
			pending, ok := a.takeSteering()
			if !ok {
				if err := a.emitAgentEnd(firstMessageIndex); err != nil {
					cancel()
					return nil, err
				}
				return final, nil
			}
			if err := a.emit(llm.TurnStartEvent{}); err != nil {
				a.restoreSteering(pending)
				cancel()
				return nil, err
			}
			initialSteering = &pending
			continue
		}

		// 6.执行工具批次，结束当前 turn 后开始下一轮模型调用
		toolResults, err := a.executeToolBatch(ctx, toolCalls)
		if err != nil {
			cancel()
			return nil, err
		}
		if err := a.emit(llm.TurnEndEvent{
			Message:     *final,
			ToolResults: toolResults,
		}); err != nil {
			cancel()
			return nil, err
		}
		if err := a.emit(llm.TurnStartEvent{}); err != nil {
			cancel()
			return nil, err
		}
	}
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
	setAssistantMetadata(&message, a.AgentConfig.Model)
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
			text, _ := message.Content[index].(llm.TextContent)
			text.Text += event.Delta
			message.Content[index] = text
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
			thinking, _ := message.Content[index].(llm.ThinkingContent)
			thinking.Thinking += event.Delta
			message.Content[index] = thinking
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
		message.Usage.Cost = llm.PriceUsage(a.AgentConfig.Model, message.Usage)
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
	messages := append([]llm.Message(nil), a.AgentContext.Messages[firstMessageIndex:]...)
	return a.emitAgentEndMessages(messages, cause...)
}

// emitAgentEndWithMessages 在恢复路径补充已提交但不属于当前内存切片的消息
func (a *Agent) emitAgentEndWithMessages(firstMessageIndex int, extra []llm.Message, cause ...error) error {
	messages := append([]llm.Message(nil), a.AgentContext.Messages[firstMessageIndex:]...)
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
	if a.OnEvent == nil {
		return nil
	}
	if err := a.OnEvent(event); err != nil {
		return fmt.Errorf("agent: event consumer failed: %w", err)
	}
	return nil
}

func (a *Agent) findTool(name string) llm.Tool {
	for _, t := range a.AgentContext.Tools {
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
	if a.CommitMessages != nil {
		var err error
		ids, err = a.CommitMessages(messages)
		if err != nil {
			return ids, err
		}
	}
	a.AgentContext.Messages = append(a.AgentContext.Messages, messages...)
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
