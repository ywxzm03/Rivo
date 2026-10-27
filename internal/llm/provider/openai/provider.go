package openai

import (
	"context"
	"fmt"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/api/openai/completions"
	"github.com/ywxzm03/Rivo/internal/llm/api/openai/responses"
)

const ProviderID = "openai"

// Config 描述一个走 OpenAI 协议的 provider；DeepSeek 等兼容厂商复用本实现
type Config struct {
	APIKey  string
	BaseURL string
	// ProviderID 决定错误前缀与默认模型归属，为空时用 ProviderID 常量
	ProviderID string
	// DefaultBaseURL 在 BaseURL 为空时生效
	DefaultBaseURL string
	// Models 为空时用内置 openai 模型列表
	Models []llm.Model
}

type Provider struct {
	id          string
	responses   *responses.Client
	completions *completions.Client
	models      []llm.Model
}

func New(cfg Config) *Provider {
	id := cfg.ProviderID
	if id == "" {
		id = ProviderID
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = cfg.DefaultBaseURL
	}
	models := cfg.Models
	if models == nil {
		models = builtinModels()
	}
	return &Provider{
		id:          id,
		responses:   responses.NewClient(cfg.APIKey, baseURL),
		completions: completions.NewClient(cfg.APIKey, baseURL),
		models:      models,
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
		return nil, fmt.Errorf("%s: unsupported protocol %q", p.id, model.API)
	}
}
