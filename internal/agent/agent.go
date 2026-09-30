package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type Agent struct {
	AgentConfig  llm.AgentConfig
	AgentContext llm.Context
	StreamFn     llm.StreamFn
	OnEvent      func(llm.Event)
}

func NewAgent(cfg llm.AgentConfig, agentCtx llm.Context, streamFn llm.StreamFn, onEvent func(event llm.Event)) *Agent {
	return &Agent{
		AgentConfig:  cfg,
		AgentContext: agentCtx,
		StreamFn:     streamFn,
		OnEvent:      onEvent,
	}
}

func (a *Agent) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	a.AgentContext.Messages = append(a.AgentContext.Messages, llm.UserMessage{
		Content: []llm.Content{llm.TextContent{Text: input}},
	})

	for {
		events, err := a.StreamFn(ctx, a.AgentConfig.Model, a.AgentContext, llm.Options{})
		if err != nil {
			return nil, err
		}

		var final *llm.AssistantMessage
		for ev := range events {
			if a.OnEvent != nil {
				a.OnEvent(ev)
			}
			switch ev.Type {
			case llm.EventDone:
				final = ev.Message
			case llm.EventError:
				return nil, ev.Err
			}
		}
		if final == nil {
			return nil, errors.New("agent: 流已结束但未收到最终消息")
		}
		a.AgentContext.Messages = append(a.AgentContext.Messages, *final)

		var toolCalls []llm.ToolCall
		for _, c := range final.Content {
			tc, ok := c.(llm.ToolCall)
			if !ok {
				continue
			}
			toolCalls = append(toolCalls, tc)
		}
		if len(toolCalls) == 0 {
			return final, nil
		}
		if err := a.executeToolBatch(ctx, toolCalls); err != nil {
			return nil, err
		}
	}
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

func (a *Agent) executeToolBatch(ctx context.Context, calls []llm.ToolCall) error {
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

func (a *Agent) executeSequentialBatch(ctx context.Context, calls []llm.ToolCall) error {
	for _, call := range calls {
		result, isError := executeToolCall(ctx, a, call)
		a.appendToolResult(call, result, isError)
	}
	return nil
}

type toolCompletion struct {
	index   int
	result  llm.ToolResult
	isError bool
}

func (a *Agent) executeParallelBatch(ctx context.Context, calls []llm.ToolCall) error {
	completed := make(chan toolCompletion, len(calls))
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(index int, toolCall llm.ToolCall) {
			defer wg.Done()
			result, isError := executeToolCall(ctx, a, toolCall)
			completed <- toolCompletion{index: index, result: result, isError: isError}
		}(i, call)
	}

	results := make([]toolCompletion, len(calls))
	for range calls {
		completion := <-completed
		results[completion.index] = completion
	}

	wg.Wait()

	for i, call := range calls {
		a.appendToolResult(call, results[i].result, results[i].isError)
	}
	return nil
}

func executeToolCall(ctx context.Context, a *Agent, call llm.ToolCall) (llm.ToolResult, bool) {
	result, err := a.execute(ctx, call.Name, call.Arguments)
	if err != nil {
		result.Content = []llm.Content{llm.TextContent{Text: "tool error: " + err.Error()}}
		result.IsError = true
	}
	return result, result.IsError
}

func (a *Agent) appendToolResult(call llm.ToolCall, result llm.ToolResult, isError bool) {
	message := llm.ToolResultMessage{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    result.Content,
		Details:    result.Details,
		IsError:    isError,
	}
	a.AgentContext.Messages = append(a.AgentContext.Messages, message)
}
