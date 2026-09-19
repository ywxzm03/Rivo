package app

import (
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

const (
	providerEnv = "PROVIDER"
	apiKeyEnv   = "API_KEY"
	baseURLEnv  = "BASE_URL"
	modelEnv    = "MODEL"
)

const defaultProviderID = deepseek.ProviderID

type providerSpec struct {
	id           string
	defaultModel string
	new          func(apiKey, baseURL string) ([]llm.Model, llm.StreamFn)
}

var providerSpecs = map[string]providerSpec{
	openai.ProviderID: {
		id:           openai.ProviderID,
		defaultModel: openai.DefaultModel,
		new: func(apiKey, baseURL string) ([]llm.Model, llm.StreamFn) {
			p := openai.New(openai.Config{APIKey: apiKey, BaseURL: baseURL})
			return p.Models(), p.Stream
		},
	},
	deepseek.ProviderID: {
		id:           deepseek.ProviderID,
		defaultModel: deepseek.DefaultModel,
		new: func(apiKey, baseURL string) ([]llm.Model, llm.StreamFn) {
			p := deepseek.New(deepseek.Config{APIKey: apiKey, BaseURL: baseURL})
			return p.Models(), p.Stream
		},
	},
}

func lookupProvider(id string) (providerSpec, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		id = defaultProviderID
	}
	spec, ok := providerSpecs[id]
	if !ok {
		return providerSpec{}, fmt.Errorf("未知的厂商 %q", id)
	}
	return spec, nil
}

func findModel(providerID string, models []llm.Model, modelID string) (llm.Model, error) {
	for _, m := range models {
		if m.ID == modelID {
			return m, nil
		}
	}
	return llm.Model{}, fmt.Errorf("厂商 %q 下没有模型 %q", providerID, modelID)
}
