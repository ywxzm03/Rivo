package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type Agent struct {
	AgentConfig  llm.AgentConfig
	AgentContext llm.Context
	StreamFn     llm.StreamFn
	OnEvent      llm.AgentEventSink
	// Compactor 为 nil 时禁用上下文压缩；独立于 AgentConfig 是为了避免 llm 包反向依赖 compaction
	Compactor *compaction.Compactor

	// lastSummary 记录最近一次压缩的摘要，供下一次压缩增量合并
	lastSummary string
	// usageStale 表示上下文里最后一条 assistant 的 usage 反映的是压缩前的大上下文，
	// 此期间阈值判断改用纯估算，防止压缩刚结束就重复触发
	usageStale bool

	runGuardOnce sync.Once
	runGuard     chan struct{}
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
	// overflowRetried 限制每次运行最多做一次溢出恢复，防止压缩后仍溢出时死循环
	overflowRetried := false
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

	for {
		// 1.5.压缩检查：响应成功但 usage 已超窗（静默溢出）或逼近阈值时，先压缩再发起本轮模型调用；
		// 压缩失败降级为失败事件，以原上下文继续运行，真实溢出由后续请求自然暴露
		if reason, ok := a.pendingCompactionReason(); ok {
			if err := a.runCompaction(ctx, reason, ""); err != nil {
				// 中止（取消或超时）直接结束运行，带着失效的 ctx 继续没有意义
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
			} else {
				// 压缩重排了消息，运行起点改记压缩后的上下文长度，保证 agent_end 汇总不越界
				firstMessageIndex = len(a.AgentContext.Messages)
			}
		}
		// 2.消费模型流并同步发出 assistant partial 更新
		partial := a.newAssistantMessage()
		if err := a.emit(llm.MessageStartEvent{Message: partial}); err != nil {
			return nil, err
		}
		// 每轮单独派生 stream ctx：错误事件分支只取消它，溢出恢复仍能拿到存活的运行级 ctx
		streamCtx, streamCancel := context.WithCancel(ctx)
		events, streamErr := a.StreamFn(streamCtx, a.AgentConfig.Model, a.AgentContext, llm.Options{})

		var final *llm.AssistantMessage
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
						streamErr = errors.New("agent: 模型流返回空错误")
					}
					// 只取消本轮流，运行级 ctx 保持存活供溢出恢复使用
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
			streamErr = errors.New("agent: 流已结束但未收到最终消息")
		}
		if streamErr != nil {
			// 溢出恢复：失败的尝试照常落盘并追加省略标记（投影上删除、磁盘上保留），压缩后重试本轮一次；
			// 同一次运行第二次溢出按普通错误收场
			if a.Compactor != nil && !overflowRetried && llm.IsContextOverflow(streamErr) {
				overflowRetried = true
				if err := a.recoverFromOverflow(ctx, streamErr, nil); err != nil {
					// 恢复失败按原始溢出错误收场，压缩失败事件已在 runCompaction 内发出
					return a.finishModelError(firstMessageIndex, streamErr)
				}
				firstMessageIndex = len(a.AgentContext.Messages)
				continue
			}
			return a.finishModelError(firstMessageIndex, streamErr)
		}
		// 3.提交最终 assistant 消息，并检查是否需要执行工具
		// 输出在达到模型上限前被截断说明上下文已耗尽，走溢出恢复；达到上限的截断是正常输出限制
		if a.Compactor != nil && !overflowRetried && isRecoverableLength(*final, a.AgentConfig.Model) {
			overflowRetried = true
			lengthErr := errors.New("agent: 上下文溢出：输出在达到模型上限前被截断")
			// 落盘的失败尝试与正常提交路径一致带上模型元数据
			setAssistantMetadata(final, a.AgentConfig.Model)
			if err := a.recoverFromOverflow(ctx, lengthErr, final); err != nil {
				// 恢复失败按普通错误收场，被截断的尝试已落盘并省略
				return a.finishModelError(firstMessageIndex, lengthErr)
			}
			firstMessageIndex = len(a.AgentContext.Messages)
			continue
		}
		setAssistantMetadata(final, a.AgentConfig.Model)
		a.AgentContext.Messages = append(a.AgentContext.Messages, *final)
		// 新 assistant 携带本次请求的真实 usage，陈旧标记就此解除
		a.usageStale = false
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

// 压缩原因，随 compaction 事件暴露给消费者
const (
	compactionReasonAuto     = "auto"
	compactionReasonOverflow = "overflow"
	compactionReasonManual   = "manual"
)

// pendingCompactionReason 判断循环顶部是否需要压缩及原因：
// 静默溢出（响应成功但 usage 已超窗）优先于阈值触发，陈旧 usage 期间改用纯估算
func (a *Agent) pendingCompactionReason() (string, bool) {
	if a.Compactor == nil {
		return "", false
	}
	window := a.Compactor.Config.ContextWindow
	if window <= 0 {
		return "", false
	}
	if !a.usageStale {
		if msg := a.lastAssistant(); msg != nil && msg.Usage.InputTokens > window {
			return compactionReasonOverflow, true
		}
	}
	if compaction.ShouldCompact(a.contextTokens(), window, a.Compactor.Config.ReserveTokens) {
		return compactionReasonAuto, true
	}
	return "", false
}

// contextTokens 按 usage 是否陈旧选择估算方式：压缩后保留尾部的 usage 反映压缩前的大上下文，
// 此时改用纯消息体估算，防止压缩刚结束就重复触发
func (a *Agent) contextTokens() int {
	if a.usageStale {
		return compaction.EstimateContextTokens(a.AgentContext.Messages)
	}
	return compaction.ContextTokens(a.AgentContext.Messages)
}

// lastAssistant 返回上下文中最后一条 assistant 消息
func (a *Agent) lastAssistant() *llm.AssistantMessage {
	for i := len(a.AgentContext.Messages) - 1; i >= 0; i-- {
		if msg, ok := a.AgentContext.Messages[i].(llm.AssistantMessage); ok {
			return &msg
		}
	}
	return nil
}

// isRecoverableLength 输出在达到模型输出上限之前被截断，说明截断来自上下文耗尽而非输出上限；
// 输出上限未知的模型无法判定，不触发恢复
func isRecoverableLength(message llm.AssistantMessage, model llm.Model) bool {
	if message.StopReason != llm.StopReasonLength || model.MaxTokens <= 0 {
		return false
	}
	return message.Usage.OutputTokens < model.MaxTokens
}

// runCompaction 执行一次压缩：1.发出开始事件；2.生成摘要（全部易失步骤集中在此）；
// 3.发出结束事件驱动 session 落盘压缩点；4.落盘成功后才替换内存上下文与 lastSummary。
// 任一步失败都会话与内存保持原样（失败不落盘），并发带 Error 的结束事件通知消费者；
// 中止（ctx 取消）静默返回，不打扰 UI
func (a *Agent) runCompaction(ctx context.Context, reason, instructions string) error {
	if a.Compactor == nil {
		return errors.New("agent: 未启用上下文压缩")
	}
	tokensBefore := a.contextTokens()
	if err := a.emit(llm.CompactionStartEvent{Reason: reason, TokensBefore: tokensBefore}); err != nil {
		return err
	}
	result, err := a.Compactor.Compact(ctx, a.AgentContext.Messages, a.lastSummary, instructions)
	if err != nil {
		return a.emitCompactionFailed(reason, tokensBefore, err)
	}
	kept := append([]llm.Message(nil), a.AgentContext.Messages[result.CutIndex:]...)
	// TokensAfter 以摘要与保留尾部的纯估算给出：保留尾部的旧 usage 是压缩前数据，不可复用
	tokensAfter := compaction.EstimateTokens(compaction.WrappedSummary(result.Summary))
	for _, msg := range kept {
		tokensAfter += compaction.MessageTokens(msg)
	}
	// 结束事件驱动 session 落盘压缩点，失败时内存尚未改动，会话与内存保持一致
	err = a.emit(llm.CompactionEndEvent{
		Reason:           reason,
		Summary:          result.Summary,
		KeptMessageCount: len(kept),
		TokensBefore:     tokensBefore,
		TokensAfter:      tokensAfter,
		Usage:            result.Usage,
	})
	if err != nil {
		return err
	}
	// 落盘成功后提交内存替换
	messages := make([]llm.Message, 0, len(kept)+1)
	messages = append(messages, llm.UserMessage{
		Content: []llm.Content{llm.TextContent{Text: compaction.WrappedSummary(result.Summary)}},
	})
	messages = append(messages, kept...)
	a.AgentContext.Messages = messages
	a.lastSummary = result.Summary
	a.usageStale = true
	return nil
}

// emitCompactionFailed 发出带 Error 的压缩结束事件通知消费者后返回原错误；中止时静默不打扰
func (a *Agent) emitCompactionFailed(reason string, tokensBefore int, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if emitErr := a.emit(llm.CompactionEndEvent{Reason: reason, TokensBefore: tokensBefore, Error: err.Error()}); emitErr != nil {
		return emitErr
	}
	return err
}

// recoverFromOverflow 溢出恢复的公共流程：落盘失败尝试并追加省略标记，再压缩重试。
// attempt 为 nil 时（流式错误路径）合成一条 error assistant 消息；失败尝试不进入内存上下文
func (a *Agent) recoverFromOverflow(ctx context.Context, streamErr error, attempt *llm.AssistantMessage) error {
	if attempt == nil {
		message := a.newAssistantMessage()
		message.StopReason = llm.StopReasonError
		message.Content = []llm.Content{llm.TextContent{Text: streamErr.Error()}}
		attempt = &message
	}
	if err := a.emit(llm.MessageEndEvent{Message: *attempt}); err != nil {
		return err
	}
	// 会话文件只追加：省略标记让失败尝试在模型投影中消失，磁盘上仍可审计回放
	if err := a.emit(llm.CompactionOmitEvent{}); err != nil {
		return err
	}
	return a.runCompaction(ctx, compactionReasonOverflow, "")
}

// CompactNow 手动触发一次压缩，instructions 为聚焦指令；与 Run 共用运行守卫避免交错
func (a *Agent) CompactNow(ctx context.Context, instructions string) error {
	release, err := a.acquireRunGuard(ctx)
	if err != nil {
		return err
	}
	defer release()
	return a.runCompaction(ctx, compactionReasonManual, instructions)
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

// finishModelError 将模型流错误记录为 assistant 消息并结束本次运行
func (a *Agent) finishModelError(firstMessageIndex int, err error) (*llm.AssistantMessage, error) {
	message := a.newAssistantMessage()
	message.StopReason = llm.StopReasonError
	message.Content = []llm.Content{llm.TextContent{Text: err.Error()}}
	a.AgentContext.Messages = append(a.AgentContext.Messages, message)
	if emitErr := a.emit(llm.MessageEndEvent{Message: message}); emitErr != nil {
		return nil, emitErr
	}
	if emitErr := a.emitAgentEnd(firstMessageIndex); emitErr != nil {
		return nil, emitErr
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
		result, isError := executeToolCall(ctx, a, call)
		if err := a.emitToolEnd(call, result, isError); err != nil {
			return nil, err
		}
		message, err := a.appendToolResult(call, result, isError)
		if err != nil {
			return nil, err
		}
		results = append(results, message)
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
			result, isError := executeToolCall(batchCtx, a, toolCall)
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
		result.Content = []llm.Content{llm.TextContent{Text: "tool error: " + err.Error()}}
		result.IsError = true
	}
	return result, result.IsError
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
