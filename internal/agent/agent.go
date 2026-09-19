package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type ExecuteFunc func(ctx context.Context, name string, args map[string]any) (string, error)

type Agent struct {
	AgentConfig  llm.AgentConfig
	AgentContext llm.Context
	StreamFn     llm.StreamFn
	Execute      ExecuteFunc
	OnEvent      func(llm.Event)
}

func NewAgent(cfg llm.AgentConfig, agentCtx llm.Context, streamFn llm.StreamFn, execute ExecuteFunc, onEvent func(event llm.Event)) *Agent {
	return &Agent{
		AgentConfig:  cfg,
		AgentContext: agentCtx,
		StreamFn:     streamFn,
		Execute:      execute,
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

		var toolResults []llm.ToolResultMessage
		for _, c := range final.Content {
			tc, ok := c.(llm.ToolCall)
			if !ok {
				continue
			}
			out, err := a.execute(ctx, tc.Name, tc.Arguments)
			if err != nil {
				out = "tool error: " + err.Error()
			}
			toolResults = append(toolResults, llm.ToolResultMessage{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Content:    []llm.Content{llm.TextContent{Text: out}},
			})
		}
		if len(toolResults) == 0 {
			return final, nil
		}
		for _, tr := range toolResults {
			a.AgentContext.Messages = append(a.AgentContext.Messages, tr)
		}
	}
}

func (a *Agent) execute(ctx context.Context, name string, args map[string]any) (string, error) {
	if a.Execute == nil {
		return "", fmt.Errorf("agent: 模型请求工具 %q 但未配置 Execute", name)
	}
	return a.Execute(ctx, name, args)
}
