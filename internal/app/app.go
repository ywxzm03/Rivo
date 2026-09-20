package app

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tool"
)

type App struct {
	agent *agent.Agent
}

func New(cfg Config, onEvent func(llm.Event)) (*App, error) {
	spec, err := lookupProvider(cfg.ProviderID)
	if err != nil {
		return nil, err
	}

	models, streamFn := spec.new(cfg.APIKey, cfg.BaseURL)
	model, err := findModel(spec.id, models, cfg.ModelID)
	if err != nil {
		return nil, err
	}

	return &App{
		agent: agent.NewAgent(
			llm.AgentConfig{Model: model},
			llm.Context{
				SystemPrompt: systemPrompt,
				Tools:        []llm.Tool{&tool.Read{SupportsImages: model.SupportsImages}},
			},
			streamFn,
			onEvent,
		),
	}, nil
}

func (a *App) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	return a.agent.Run(ctx, input)
}
