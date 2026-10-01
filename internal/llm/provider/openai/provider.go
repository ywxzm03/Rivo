package openai

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/api/openai/responses"
)

const ProviderID = "openai"

type Config struct {
	APIKey  string
	BaseURL string
}

type Provider struct {
	client *responses.Client
	models []llm.Model
}

func New(cfg Config) *Provider {
	return &Provider{
		client: responses.NewClient(cfg.APIKey, cfg.BaseURL),
		models: builtinModels(),
	}
}

func (p *Provider) Models() []llm.Model { return p.models }

func (p *Provider) Stream(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.Event, error) {
	return p.client.Stream(ctx, model, c, opts)
}
