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
}

func NewAgent(cfg llm.AgentConfig, agentCtx llm.Context, streamFn llm.StreamFn, onEvent llm.AgentEventSink) *Agent {
	return &Agent{
		AgentConfig:  cfg,
		AgentContext: agentCtx,
		StreamFn:     streamFn,
		OnEvent:      onEvent,
	}
}

// Run 执行一轮或多轮模型调用，并在需要时执行模型返回的工具
func (a *Agent) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
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

	for {
		// 2.消费模型流并同步发出 assistant partial 更新
		partial := a.newAssistantMessage()
		if err := a.emit(llm.MessageStartEvent{Message: partial}); err != nil {
			return nil, err
		}
		events, streamErr := a.StreamFn(ctx, a.AgentConfig.Model, a.AgentContext, llm.Options{})

		var final *llm.AssistantMessage
		toolCallArguments := make(map[int]string)
		if streamErr == nil {
			for ev := range events {
				switch ev.Type {
				case llm.EventStart:
					if ev.Partial != nil {
						partial = *ev.Partial
					}
				case llm.EventTextStart, llm.EventTextDelta, llm.EventTextEnd,
					llm.EventThinkingStart, llm.EventThinkingDelta, llm.EventThinkingEnd,
					llm.EventToolCallStart, llm.EventToolCallDelta, llm.EventToolCallEnd, llm.EventToolCall:
					if ev.Partial != nil {
						partial = *ev.Partial
					} else {
						appendAssistantEvent(&partial, ev, toolCallArguments)
					}
					if err := a.emit(llm.MessageUpdateEvent{
						Message:               partial,
						AssistantMessageEvent: ev,
					}); err != nil {
						cancel()
						return nil, err
					}
				case llm.EventDone:
					final = ev.Message
				case llm.EventError:
					streamErr = ev.Err
					if streamErr == nil {
						streamErr = errors.New("agent: 模型流返回空错误")
					}
					cancel()
				}
				if streamErr != nil {
					break
				}
			}
		}
		if streamErr == nil && final == nil {
			streamErr = errors.New("agent: 流已结束但未收到最终消息")
		}
		if streamErr != nil {
			return a.finishModelError(firstMessageIndex, streamErr)
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
func appendAssistantEvent(message *llm.AssistantMessage, event llm.Event, toolCallArguments map[int]string) {
	index := event.ContentIndex
	switch event.Type {
	case llm.EventTextStart:
		setAssistantContent(message, index, llm.TextContent{})
	case llm.EventTextDelta:
		if ensureAssistantContentSlot(message, index) {
			text, _ := message.Content[index].(llm.TextContent)
			text.Text += event.Delta
			message.Content[index] = text
		}
	case llm.EventTextEnd:
		setAssistantContent(message, index, llm.TextContent{Text: event.Content})
	case llm.EventThinkingStart:
		setAssistantContent(message, index, llm.ThinkingContent{})
	case llm.EventThinkingDelta:
		if ensureAssistantContentSlot(message, index) {
			thinking, _ := message.Content[index].(llm.ThinkingContent)
			thinking.Thinking += event.Delta
			message.Content[index] = thinking
		}
	case llm.EventThinkingEnd:
		setAssistantContent(message, index, llm.ThinkingContent{Thinking: event.Content})
	case llm.EventToolCallStart:
		setAssistantContent(message, index, event.ToolCall)
	case llm.EventToolCallDelta:
		toolCallArguments[index] += event.Delta
		var arguments map[string]any
		if json.Unmarshal([]byte(toolCallArguments[index]), &arguments) == nil && ensureAssistantContentSlot(message, index) {
			call, ok := message.Content[index].(llm.ToolCall)
			if !ok {
				call = llm.ToolCall{}
			}
			call.Arguments = arguments
			message.Content[index] = call
		}
	case llm.EventToolCallEnd:
		setAssistantContent(message, index, event.ToolCall)
	case llm.EventToolCall:
		message.Content = append(message.Content, event.ToolCall)
	}
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
