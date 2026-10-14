package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type Agent struct {
	AgentConfig  llm.AgentConfig
	AgentContext llm.Context
	StreamFn     llm.StreamFn
	OnEvent      llm.AgentEventSink
	// Coordinator 为压缩编排层回调：循环顶的阈值压缩与溢出通知经它交给编排层处理，
	// Agent 自身不做压缩决策与持久化；nil 时禁用压缩
	Coordinator CompactionCoordinator

	runGuardOnce sync.Once
	runGuard     chan struct{}
}

// CompactionCoordinator 由编排层实现（session 包），Agent 在轮次边界回调；
// 持久化与投影重建都在编排层完成，Agent 用替换后的消息序列继续运行
type CompactionCoordinator interface {
	// CompactIfNeeded 评估会话投影的阈值与静默溢出，需要时压缩、重建并替换内存，返回是否发生了压缩
	CompactIfNeeded(ctx context.Context) (compacted bool, err error)
	// NotifyOverflow 通知编排层本轮以可恢复的上下文溢出收场（显式溢出错误或可恢复的长度截断）
	NotifyOverflow()
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
		// 守卫就绪与 ctx 取消同时发生时 select 会随机选择分支，需复查取消避免已取消的运行抢占守卫
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
	// 同一 Agent 的多次运行在此排队串行执行，等待期间响应取消
	release, err := a.acquireRunGuard(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	firstMessageIndex := len(a.AgentContext.Messages)
	if err := a.emit(llm.AgentStartEvent{}); err != nil {
		return nil, err
	}

	// 1.发出用户消息事件并写入上下文
	userMessage := llm.UserMessage{
		Content: []llm.Content{llm.TextContent{Text: input}},
	}
	if err := a.emit(llm.TurnStartEvent{}); err != nil {
		return nil, err
	}
	if err := a.emit(llm.MessageStartEvent{Message: userMessage}); err != nil {
		return nil, err
	}
	a.AgentContext.Messages = append(a.AgentContext.Messages, userMessage)
	if err := a.emit(llm.MessageEndEvent{Message: userMessage}); err != nil {
		return nil, err
	}

	return a.runLoop(ctx, cancel, firstMessageIndex)
}

// Continue 在不追加用户消息的情况下重入运行循环，供编排层在溢出恢复后重试该轮
func (a *Agent) Continue(ctx context.Context) (*llm.AssistantMessage, error) {
	release, err := a.acquireRunGuard(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	firstMessageIndex := len(a.AgentContext.Messages)
	if err := a.emit(llm.AgentStartEvent{}); err != nil {
		return nil, err
	}
	if err := a.emit(llm.TurnStartEvent{}); err != nil {
		return nil, err
	}
	return a.runLoop(ctx, cancel, firstMessageIndex)
}

// ReplaceMessages 用编排层提供的投影消息替换内存消息序列（Tools 不变）
func (a *Agent) ReplaceMessages(messages []llm.Message) {
	a.AgentContext.Messages = messages
}

// AppendMessage 把编排层产生的消息（如 system prompt 补丁）压入内存消息序列
func (a *Agent) AppendMessage(msg llm.Message) {
	a.AgentContext.Messages = append(a.AgentContext.Messages, msg)
}

// runLoop 消费模型流并执行工具，循环顶回调编排层做阈值压缩
func (a *Agent) runLoop(ctx context.Context, cancel context.CancelFunc, firstMessageIndex int) (*llm.AssistantMessage, error) {
	for {
		// 1.压缩检查：由编排层在会话投影上评估并执行（静默溢出优先于阈值），压缩后内存已被替换；
		// 压缩失败降级为失败事件，以原上下文继续运行，真实溢出由后续请求自然暴露
		if a.Coordinator != nil {
			compacted, err := a.Coordinator.CompactIfNeeded(ctx)
			if err != nil {
				// 中止（取消或超时）：以 aborted 事件收场，保持与其他取消路径一致的 UI 语义
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return a.finishModelFailure(firstMessageIndex, llm.StopReasonAborted, err, nil)
				}
			} else if compacted {
				// 压缩重排了消息，运行起点改记压缩后的上下文长度，保证 agent_end 汇总不越界
				firstMessageIndex = len(a.AgentContext.Messages)
			}
		}
		// 2.消费模型流并同步发出 assistant partial 更新
		partial := a.newAssistantMessage()
		if err := a.emit(llm.MessageStartEvent{Message: partial}); err != nil {
			return nil, err
		}
		// 每轮单独派生 stream ctx：错误事件分支只取消它，运行级 ctx 保持存活
		streamCtx, streamCancel := context.WithCancel(ctx)
		requestContext := contextForModel(a.AgentContext)
		events, streamErr := a.StreamFn(streamCtx, a.AgentConfig.Model, requestContext, llm.Options{ThinkingLevel: a.AgentConfig.ThinkingLevel})

		var final *llm.AssistantMessage
		var streamReason llm.StopReason
		toolCallArguments := make(map[int]string)
		if streamErr == nil {
			for event := range events {
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
						streamCancel()
						return nil, err
					}
				case llm.AssistantMessageDoneEvent:
					final = ev.Message
				case llm.AssistantMessageErrorEvent:
					streamErr = ev.Err
					if streamErr == nil {
						streamErr = errors.New("agent: model stream returned empty error")
					}
					// 流层区分取消与真实失败：aborted 走事件收场，error 保留错误上抛
					streamReason = ev.Reason
					if streamReason != llm.StopReasonAborted && streamReason != llm.StopReasonError {
						streamReason = llm.StopReasonError
					}
					if ev.Partial != nil {
						partial = cloneAssistantMessage(*ev.Partial)
					}
					// 只取消本轮流，运行级 ctx 保持存活
					streamCancel()
				}
				if streamErr != nil {
					break
				}
			}
		}
		// 本轮流已结束（正常关闭或已中止），统一收尾
		streamCancel()
		if streamErr == nil && final == nil {
			// 流层静默关闭（防御路径）：取消时以 aborted 收场，其余按真实缺陷报告
			if errors.Is(streamCtx.Err(), context.Canceled) || errors.Is(streamCtx.Err(), context.DeadlineExceeded) {
				streamReason = llm.StopReasonAborted
			} else {
				streamErr = errors.New("agent: stream ended without a final message")
			}
		}
		if streamErr != nil || streamReason == llm.StopReasonAborted {
			// 可恢复的溢出：通知编排层驱动轮外恢复（它会标注 willRetry 并省略失败尝试），本轮按错误收场
			if a.Coordinator != nil && llm.IsContextOverflow(streamErr) {
				a.Coordinator.NotifyOverflow()
			}
			reason := streamReason
			if reason != llm.StopReasonAborted {
				reason = llm.StopReasonError
			}
			return a.finishModelFailure(firstMessageIndex, reason, streamErr, &partial)
		}
		// 3.提交最终 assistant 消息，并检查是否需要执行工具
		setAssistantMetadata(final, a.AgentConfig.Model)
		a.AgentContext.Messages = append(a.AgentContext.Messages, *final)
		if err := a.emit(llm.MessageEndEvent{Message: *final}); err != nil {
			cancel()
			return nil, err
		}

		var toolCalls []llm.ToolCall
		for _, c := range final.Content {
			tc, ok := c.(llm.ToolCall)
			if !ok {
				continue
			}
			toolCalls = append(toolCalls, tc)
		}
		// 3.5.输出在达到模型上限前被截断且启用了编排层：跳过截断消息里的工具调用，
		// 正常提交本轮后通知编排层驱动溢出恢复；未启用编排层时按旧语义继续执行工具；
		// 达到上限的截断是正常输出限制
		if a.Coordinator != nil && isRecoverableLength(*final, a.AgentConfig.Model) {
			if a.Coordinator != nil {
				a.Coordinator.NotifyOverflow()
			}
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
			if err := a.emitAgentEnd(firstMessageIndex); err != nil {
				cancel()
				return nil, err
			}
			return final, nil
		}

		// 4.执行工具批次，结束当前 turn 后开始下一轮模型调用
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

// contextForModel 过滤已经错误收尾的 assistant，避免取消/失败文本污染下一次请求
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

// isRecoverableLength 输出在达到模型输出上限之前被截断，说明截断来自上下文耗尽而非输出上限；
// 输出上限未知的模型无法判定，不触发恢复
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

// setAssistantMetadata 写入 assistant 消息的模型标识
func setAssistantMetadata(message *llm.AssistantMessage, model llm.Model) {
	message.API = model.API
	message.Provider = model.Provider
	message.Model = model.ID
}

// appendAssistantEvent 将无快照的模型事件累积到对应内容索引
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
		// delta事件发送的增量是多段的，是不完整的JSON；需要一直更新当前值，直到JSON反序列化成功，这就说明工具参数发送完整了，tool_call完整了
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

// finishModelFailure 将模型流终止记录为 assistant 消息并结束本次运行；
// aborted 保留已收到的 partial 内容，error 记录错误文本（对齐 Pi 的 stopReason 语义）
func (a *Agent) finishModelFailure(firstMessageIndex int, reason llm.StopReason, err error, partial *llm.AssistantMessage) (*llm.AssistantMessage, error) {
	message := a.newAssistantMessage()
	message.StopReason = reason
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
	a.AgentContext.Messages = append(a.AgentContext.Messages, message)
	if emitErr := a.emit(llm.MessageEndEvent{Message: message}); emitErr != nil {
		return nil, emitErr
	}
	if emitErr := a.emitAgentEnd(firstMessageIndex); emitErr != nil {
		return nil, emitErr
	}
	if reason == llm.StopReasonAborted {
		// 取消不是运行错误：事件收场后向调用方返回 nil 错误
		return &message, nil
	}
	return &message, err
}

// emitAgentEnd 汇总本次运行新增的消息并发出 agent_end
func (a *Agent) emitAgentEnd(firstMessageIndex int) error {
	messages := append([]llm.Message(nil), a.AgentContext.Messages[firstMessageIndex:]...)
	return a.emit(llm.AgentEndEvent{Messages: messages})
}

// emit 同步调用事件消费者并包装其返回错误
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
	for _, call := range calls {
		if err := a.emitToolStart(call); err != nil {
			return nil, err
		}
		var result llm.ToolResult
		var isError bool
		if ctx.Err() != nil {
			// 已取消：剩余工具不再执行，以 aborted 结果收场（对齐 Pi 的 signal 检查）
			result, isError = abortedToolResult(), true
		} else {
			result, isError = executeToolCall(ctx, a, call)
		}
		if err := a.emitToolEnd(call, result, isError); err != nil {
			return nil, err
		}
		message, err := a.appendToolResult(call, result, isError)
		if err != nil {
			return nil, err
		}
		results = append(results, message)
		if isError && ctx.Err() != nil {
			break
		}
	}
	return results, nil
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
				// 已取消：工具不再启动，以 aborted 结果收场（对齐 Pi 的启动前检查）
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
	if eventErr != nil {
		return nil, eventErr
	}

	messages := make([]llm.ToolResultMessage, 0, len(calls))
	for i, call := range calls {
		message, err := a.appendToolResult(call, results[i].result, results[i].isError)
		if err != nil {
			cancel()
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func executeToolCall(ctx context.Context, a *Agent, call llm.ToolCall) (llm.ToolResult, bool) {
	result, err := a.execute(ctx, call.Name, call.Arguments)
	if err != nil {
		if ctx.Err() != nil {
			// 取消是用户操作：统一为固定文案，与 Pi 的 createErrorToolResult("Operation aborted") 一致
			result.Content = []llm.Content{llm.TextContent{Text: "Operation aborted"}}
		} else {
			result.Content = []llm.Content{llm.TextContent{Text: "tool error: " + err.Error()}}
		}
		result.IsError = true
	}
	return result, result.IsError
}

// abortedToolResult 生成取消后未执行工具的结构化错误结果
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

// appendToolResult 发出工具结果消息事件并追加到 Agent 上下文
func (a *Agent) appendToolResult(call llm.ToolCall, result llm.ToolResult, isError bool) (llm.ToolResultMessage, error) {
	message := llm.ToolResultMessage{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    result.Content,
		Details:    result.Details,
		IsError:    isError,
	}
	if err := a.emit(llm.MessageStartEvent{Message: message}); err != nil {
		return llm.ToolResultMessage{}, err
	}
	a.AgentContext.Messages = append(a.AgentContext.Messages, message)
	if err := a.emit(llm.MessageEndEvent{Message: message}); err != nil {
		return message, err
	}
	return message, nil
}
