package app

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

type App struct {
	agent *agent.Agent
}

func New(cfg Config, onEvent func(llm.Event)) (*App, error) {
	provider := openai.New(openai.Config{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
	})

	models := llm.NewModels()
	models.Register(provider)

	model, err := models.GetModel(cfg.ProviderID, cfg.ModelID)
	if err != nil {
		return nil, err
	}
	streamFn, err := models.StreamFn(cfg.ProviderID, cfg.ModelID)
	if err != nil {
		return nil, err
	}

	return &App{
		agent: agent.NewAgent(
			llm.AgentConfig{Model: model},
			llm.Context{
				SystemPrompt: systemPrompt,
				Tools:        agent.DemoTools(),
			},
			streamFn,
			agent.DemoExecute,
			onEvent,
		),
	}, nil
}

func (a *App) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	return a.agent.Run(ctx, input)
}
