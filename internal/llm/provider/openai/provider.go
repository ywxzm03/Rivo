package openai

import (
	"context"
	"fmt"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/api/openai/completions"
	"github.com/ywxzm03/Rivo/internal/llm/api/openai/responses"
)

const ProviderID = "openai"

type Config struct {
	APIKey  string
	BaseURL string
}

type Provider struct {
	responses   *responses.Client
	completions *completions.Client
	models      []llm.Model
}

func New(cfg Config) *Provider {
	return &Provider{
		responses:   responses.NewClient(cfg.APIKey, cfg.BaseURL),
		completions: completions.NewClient(cfg.APIKey, cfg.BaseURL),
		models:      builtinModels(),
	}
}

func (p *Provider) Models() []llm.Model { return p.models }

func (p *Provider) Stream(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
	switch model.API {
	case llm.APIOpenAIResponses:
		return p.responses.Stream(ctx, model, c, opts)
	case llm.APIOpenAICompletions:
		return p.completions.Stream(ctx, model, c, opts)
	default:
		return nil, fmt.Errorf("openai: unsupported protocol %q", model.API)
	}
}
