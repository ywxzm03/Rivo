package agent

import (
	"context"
	"errors"
	"fmt"

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

		var toolResults []llm.ToolResultMessage
		for _, c := range final.Content {
			tc, ok := c.(llm.ToolCall)
			if !ok {
				continue
			}
			res, err := a.execute(ctx, tc.Name, tc.Arguments)
			if err != nil {
				res = "tool error: " + err.Error()
			}
			toolResults = append(toolResults, llm.ToolResultMessage{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Content:    []llm.Content{llm.TextContent{Text: res}},
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

func (a *Agent) findTool(name string) llm.Tool {
	for _, t := range a.AgentContext.Tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func (a *Agent) execute(ctx context.Context, name string, args map[string]any) (string, error) {
	t := a.findTool(name)
	if t == nil {
		return "", fmt.Errorf("agent: no tool named %q", name)
	}
	return t.Execute(ctx, args)
}
