package provider

import (
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

const defaultProviderID = deepseek.ProviderID

const (
	defaultContextWindow = 128000
	defaultMaxTokens     = 16384
)

type providerSpec struct {
	id           string
	defaultModel string
	// apiKey/baseURL 是解析后的凭据与端点
	apiKey  string
	baseURL string
	models  []llm.Model
	new     func(apiKey, baseURL string) ([]llm.Model, llm.StreamFn)
}

// builtinRegistry 返回内置 provider 注册表，凭据来自 <PROVIDER>_API_KEY 环境变量
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
		// 内置模型列表供 settings 成对指定的模型校验使用
		if len(spec.models) == 0 {
			if models, _ := spec.new(spec.apiKey, spec.baseURL); len(models) > 0 {
				spec.models = models
			}
		}
		specs[id] = spec
	}
	return specs
}

func customSpec(id string, p config.Provider) (providerSpec, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if len(p.Models) == 0 {
		return providerSpec{}, fmt.Errorf("provider %q has no models", id)
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return providerSpec{}, fmt.Errorf("provider %q is missing baseUrl", id)
	}
	if err := validateModelOverrides(id, p.ModelOverrides); err != nil {
		return providerSpec{}, err
	}
	models := make([]llm.Model, 0, len(p.Models))
	for _, m := range p.Models {
		model, err := modelFromConfig(id, p, m, nil)
		if err != nil {
			return providerSpec{}, err
		}
		models = append(models, model)
	}
	models = applyModelOverrides(models, p.ModelOverrides)
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

func overlaySpec(base providerSpec, p config.Provider) (providerSpec, error) {
	if err := validateModelOverrides(base.id, p.ModelOverrides); err != nil {
		return providerSpec{}, err
	}
	spec := base
	if p.APIKey != "" {
		spec.apiKey = p.APIKey
	}
	if p.BaseURL != "" {
		spec.baseURL = p.BaseURL
	}
	models := cloneModels(base.models)
	for _, definition := range p.Models {
		index := findModelIndex(models, definition.ID)
		var defaults *llm.Model
		if index >= 0 {
			defaults = &models[index]
		} else if len(models) > 0 {
			defaults = &models[0]
		}
		model, err := modelFromConfig(base.id, p, definition, defaults)
		if err != nil {
			return providerSpec{}, err
		}
		if index >= 0 {
			models[index] = model
		} else {
			models = append(models, model)
		}
	}
	spec.models = applyModelOverrides(models, p.ModelOverrides)
	return withModelList(spec, base), nil
}

func validateModelOverrides(providerID string, overrides map[string]config.ModelOverride) error {
	for modelID, override := range overrides {
		if override.ContextWindow != nil && *override.ContextWindow <= 0 {
			return fmt.Errorf("provider %q model %q has invalid contextWindow override", providerID, modelID)
		}
		if override.Cost != nil && (negative(override.Cost.Input) || negative(override.Cost.Output) || negative(override.Cost.CacheRead) || negative(override.Cost.CacheWrite)) {
			return fmt.Errorf("provider %q model %q has negative cost override", providerID, modelID)
		}
	}
	return nil
}

func negative(value *float64) bool {
	return value != nil && *value < 0
}

// withModelList 保留原 provider 的协议流函数，只替换模型元数据
func withModelList(spec, base providerSpec) providerSpec {
	models := cloneModels(spec.models)
	spec.new = func(apiKey, baseURL string) ([]llm.Model, llm.StreamFn) {
		_, streamFn := base.new(apiKey, baseURL)
		return cloneModels(models), streamFn
	}
	return spec
}

// modelFromConfig 将 models.json 模型定义转换为运行时模型
func modelFromConfig(providerID string, provider config.Provider, definition config.Model, defaults *llm.Model) (llm.Model, error) {
	if definition.HasContextWindow() && definition.ContextWindow <= 0 {
		return llm.Model{}, fmt.Errorf("provider %q model %q has invalid contextWindow", providerID, definition.ID)
	}
	api := definition.API
	if api == "" {
		if provider.HasAPI() {
			api = provider.API
		}
	}
	if api == "" && defaults != nil {
		api = string(defaults.API)
	}
	if api == "" {
		api = config.APIOpenAIResponses
	}
	contextWindow := definition.ContextWindow
	if !definition.HasContextWindow() {
		contextWindow = defaultContextWindow
	}
	maxTokens := definition.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	name := definition.Name
	if name == "" {
		name = definition.ID
	}
	var cost *llm.CostRates
	if definition.Cost != nil {
		copy := *definition.Cost
		cost = &copy
	}
	model := llm.Model{
		ID:               definition.ID,
		Name:             name,
		Provider:         providerID,
		API:              llm.API(api),
		Cost:             cost,
		Reasoning:        definition.Reasoning,
		SupportsImages:   definition.SupportsImages || hasImageInput(definition.Input),
		ContextWindow:    contextWindow,
		MaxTokens:        maxTokens,
		ThinkingLevelMap: thinkingLevelMap(definition.ThinkingLevelMap),
		SamplingParams:   copyParams(definition.SamplingParams),
	}
	if defaults != nil {
		model.ReasoningEffort = defaults.ReasoningEffort
	}
	return model, nil
}

func applyModelOverrides(models []llm.Model, overrides map[string]config.ModelOverride) []llm.Model {
	for i := range models {
		override, ok := overrides[models[i].ID]
		if ok {
			models[i] = applyModelOverride(models[i], override)
		}
	}
	return models
}

func applyModelOverride(model llm.Model, override config.ModelOverride) llm.Model {
	model = model.Clone()
	if override.Name != nil {
		model.Name = *override.Name
	}
	if override.Reasoning != nil {
		model.Reasoning = *override.Reasoning
	}
	if override.Input != nil {
		model.SupportsImages = hasImageInput(override.Input)
	}
	if override.Cost != nil {
		cost := llm.CostRates{}
		if model.Cost != nil {
			cost = *model.Cost
		}
		if override.Cost.Input != nil {
			cost.Input = *override.Cost.Input
		}
		if override.Cost.Output != nil {
			cost.Output = *override.Cost.Output
		}
		if override.Cost.CacheRead != nil {
			cost.CacheRead = *override.Cost.CacheRead
		}
		if override.Cost.CacheWrite != nil {
			cost.CacheWrite = *override.Cost.CacheWrite
		}
		model.Cost = &cost
	}
	if override.ContextWindow != nil {
		model.ContextWindow = *override.ContextWindow
	}
	if override.MaxTokens != nil {
		model.MaxTokens = *override.MaxTokens
	}
	if override.ThinkingLevelMap != nil {
		if model.ThinkingLevelMap == nil {
			model.ThinkingLevelMap = make(map[llm.ThinkingLevel]*llm.ReasoningEffort)
		}
		for level, effort := range thinkingLevelMap(override.ThinkingLevelMap) {
			model.ThinkingLevelMap[level] = effort
		}
	}
	if override.SamplingParams != nil {
		if model.SamplingParams == nil {
			model.SamplingParams = make(map[string]any)
		}
		for key, value := range override.SamplingParams {
			model.SamplingParams[key] = value
		}
	}
	return model
}

func findModelIndex(models []llm.Model, id string) int {
	for i := range models {
		if models[i].ID == id {
			return i
		}
	}
	return -1
}

func cloneModels(models []llm.Model) []llm.Model {
	cloned := make([]llm.Model, len(models))
	for i, model := range models {
		cloned[i] = model.Clone()
	}
	return cloned
}

// hasImageInput 兼容 models.json 的 input 能力声明
func hasImageInput(inputs []string) bool {
	for _, input := range inputs {
		if strings.EqualFold(strings.TrimSpace(input), "image") {
			return true
		}
	}
	return false
}

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

// FindModel 按 ID 在模型目录里查找，找不到时返回带 provider 名的错误
func FindModel(providerID string, models []llm.Model, modelID string) (llm.Model, error) {
	for _, m := range models {
		if m.ID == modelID {
			return m, nil
		}
	}
	return llm.Model{}, fmt.Errorf("provider %q has no model %q", providerID, modelID)
}

// copyParams 浅拷贝采样参数 map，避免配置与运行时共享同一份可写映射
func copyParams(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}
	cloned := make(map[string]any, len(params))
	for key, value := range params {
		cloned[key] = value
	}
	return cloned
}
