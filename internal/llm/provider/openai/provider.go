package openai

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/llm"
	openaiapi "github.com/ywxzm03/Rivo/internal/llm/api/openai"
)

const ProviderID = "openai"

type Config struct {
	APIKey  string
	BaseURL string
}

type Provider struct {
	client *openaiapi.Client
	models []llm.Model
}

func New(cfg Config) *Provider {
	return &Provider{
		client: openaiapi.NewClient(cfg.APIKey, cfg.BaseURL),
		models: builtinModels(),
	}
}

func (p *Provider) ID() string { return ProviderID }

func (p *Provider) Models() []llm.Model { return p.models }

func (p *Provider) Stream(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.Event, error) {
	return p.client.Stream(ctx, model, c, opts)
}
