package app

import (
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

const defaultProviderID = deepseek.ProviderID

type providerSpec struct {
	id           string
	defaultModel string
	// apiKey/baseURL 是解析后的凭据与端点；new 用它们装配协议客户端
	apiKey  string
	baseURL string
	models  []llm.Model
	new     func(apiKey, baseURL string) ([]llm.Model, llm.StreamFn)
}

// builtinRegistry 返回内置 provider 注册表；凭据来自 <PROVIDER>_API_KEY 环境变量
func builtinRegistry() map[string]providerSpec {
	specs := map[string]providerSpec{
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
	for id, spec := range specs {
		spec.apiKey = apiKeyFromEnv(id)
		// 填充内置模型列表，供 settings 成对指定的模型校验使用（构造客户端为离线操作）
		if len(spec.models) == 0 {
			if models, _ := spec.new(spec.apiKey, spec.baseURL); len(models) > 0 {
				spec.models = models
			}
		}
		specs[id] = spec
	}
	return specs
}

// customSpec 把 models.json 的 provider 条目转成 openai 兼容的 providerSpec
func customSpec(id string, p config.Provider) (providerSpec, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if len(p.Models) == 0 {
		return providerSpec{}, fmt.Errorf("provider %q has no models", id)
	}
	models := make([]llm.Model, 0, len(p.Models))
	for _, m := range p.Models {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		api := p.API
		if m.API != "" {
			api = m.API
		}
		models = append(models, llm.Model{
			ID:               m.ID,
			Name:             name,
			Provider:         id,
			API:              llm.API(api),
			Reasoning:        m.Reasoning,
			SupportsImages:   m.SupportsImages,
			ContextWindow:    m.ContextWindow,
			MaxTokens:        m.MaxTokens,
			ThinkingLevelMap: thinkingLevelMap(m.ThinkingLevelMap),
			SamplingParams:   m.SamplingParams,
		})
	}
	return providerSpec{
		id:           id,
		defaultModel: models[0].ID,
		apiKey:       p.APIKey,
		baseURL:      p.BaseURL,
		models:       models,
		new: func(apiKey, baseURL string) ([]llm.Model, llm.StreamFn) {
			p := openai.New(openai.Config{APIKey: apiKey, BaseURL: baseURL})
			return models, p.Stream
		},
	}, nil
}

// thinkingLevelMap 把 config 的字符串映射转为 llm 的强类型映射
func thinkingLevelMap(m map[string]*string) map[llm.ThinkingLevel]*llm.ReasoningEffort {
	if m == nil {
		return nil
	}
	out := make(map[llm.ThinkingLevel]*llm.ReasoningEffort, len(m))
	for level, effort := range m {
		if effort == nil {
			out[llm.ThinkingLevel(level)] = nil
			continue
		}
		e := llm.ReasoningEffort(*effort)
		out[llm.ThinkingLevel(level)] = &e
	}
	return out
}

// findModel 在模型列表中按 ID 查找
func findModel(providerID string, models []llm.Model, modelID string) (llm.Model, error) {
	for _, m := range models {
		if m.ID == modelID {
			return m, nil
		}
	}
	return llm.Model{}, fmt.Errorf("provider %q has no model %q", providerID, modelID)
}
