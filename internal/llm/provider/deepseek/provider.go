package deepseek

import (
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

const ProviderID = "deepseek"

const DefaultBaseURL = "https://api.deepseek.com"

type Config struct {
	APIKey  string
	BaseURL string
}

// New 返回一个指向 DeepSeek 的 OpenAI 协议客户端
func New(cfg Config) *openai.Provider {
	return openai.New(openai.Config{
		APIKey:         cfg.APIKey,
		BaseURL:        cfg.BaseURL,
		ProviderID:     ProviderID,
		DefaultBaseURL: DefaultBaseURL,
		Models:         builtinModels(),
	})
}
