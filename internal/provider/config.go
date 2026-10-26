package provider

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

// Config 是解析完成的运行配置：默认模型 + provider 注册表
type Config struct {
	ProviderID    string
	ModelID       string
	ThinkingLevel llm.ThinkingLevel
	// Warnings 是加载期的非致命警告，由调用方输出到 stderr
	Warnings []string
	// registry 是内置与 models.json 自定义 provider 的合并注册表
	registry map[string]providerSpec
}

// AvailableModels 返回已配置 provider 的模型目录快照，顺序稳定且不暴露凭据
func (c Config) AvailableModels() []llm.Model {
	var models []llm.Model
	for _, spec := range c.registry {
		models = append(models, spec.models...)
	}
	slices.SortFunc(models, func(a, b llm.Model) int {
		if a.Provider != b.Provider {
			return strings.Compare(a.Provider, b.Provider)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return models
}

// Load 从 agent dir 的 settings.json 与 models.json 解析运行配置：
// 1.构建注册表（内置 openai/deepseek + models.json provider 叠加）
// 2.按三级链解析默认模型：settings 成对指定 → 内置默认 deepseek → 首个有凭据的 provider
// 3.配置文件解析失败时记录警告、按空配置继续，最终无可用模型才报错
func Load() (Config, error) {
	cfg := Config{registry: builtinRegistry()}

	settings, err := config.LoadSettings()
	if err != nil {
		cfg.Warnings = append(cfg.Warnings, err.Error())
	}
	models, err := config.LoadModels()
	if err != nil {
		cfg.Warnings = append(cfg.Warnings, err.Error())
	}

	// 1.合并 models.json provider：同名内置 provider 叠加，未知 provider 作为自定义注册
	var order []string
	for _, np := range models.Providers {
		id := strings.ToLower(np.ID)
		base, builtin := cfg.registry[id]
		if builtin {
			spec, err := overlaySpec(base, np.Provider)
			if err != nil {
				cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("config: provider %q overlay ignored: %v", np.ID, err))
				continue
			}
			cfg.registry[id] = spec
		} else {
			spec, err := customSpec(np.ID, np.Provider)
			if err != nil {
				return Config{}, err
			}
			cfg.registry[id] = spec
		}
		order = append(order, id)
	}

	// 2.默认思考等级：非法值警告后忽略
	if settings.DefaultThinkingLevel != "" {
		if !knownThinkingLevel(settings.DefaultThinkingLevel) {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("config: unknown defaultThinkingLevel %q ignored", settings.DefaultThinkingLevel))
		} else {
			cfg.ThinkingLevel = llm.ThinkingLevel(settings.DefaultThinkingLevel)
		}
	}

	// 3.三级链解析默认模型
	// 3.1 settings 成对指定：模型不存在或无凭据时软回退
	if settings.DefaultProvider != "" && settings.DefaultModel != "" {
		spec, err := cfg.lookupProvider(settings.DefaultProvider)
		if err == nil {
			if _, err := FindModel(spec.id, spec.models, settings.DefaultModel); err == nil && spec.apiKey != "" {
				cfg.ProviderID, cfg.ModelID = spec.id, settings.DefaultModel
				return cfg, nil
			}
		}
	}
	// 3.2 内置默认 provider（deepseek）有凭据时用其默认模型
	if spec, ok := cfg.registry[defaultProviderID]; ok && spec.apiKey != "" {
		cfg.ProviderID, cfg.ModelID = spec.id, spec.defaultModel
		return cfg, nil
	}
	// 3.3 首个有凭据的 provider：models.json 文件顺序优先，其后内置 openai
	// （models.json 已定义 openai 时不重复追加）
	if !slices.Contains(order, openai.ProviderID) {
		order = append(order, openai.ProviderID)
	}
	for _, id := range order {
		if spec, ok := cfg.registry[id]; ok && spec.apiKey != "" {
			cfg.ProviderID, cfg.ModelID = spec.id, spec.defaultModel
			return cfg, nil
		}
	}
	dir, err := config.Dir()
	if err != nil {
		return Config{}, err
	}
	message := fmt.Sprintf("no models available: configure a provider in %s/models.json", dir)
	if len(cfg.Warnings) > 0 {
		// 把原始降级原因并入错误，避免掩盖真实问题
		message += "; config warnings: " + strings.Join(cfg.Warnings, "; ")
	}
	return Config{}, fmt.Errorf("%s", message)
}

func knownThinkingLevel(level string) bool {
	switch llm.ThinkingLevel(level) {
	case llm.ThinkingLevelOff, llm.ThinkingLevelMinimal, llm.ThinkingLevelLow, llm.ThinkingLevelMedium,
		llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh, llm.ThinkingLevelMax:
		return true
	default:
		return false
	}
}

func (c Config) lookupProvider(id string) (providerSpec, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		id = defaultProviderID
	}
	spec, ok := c.registry[id]
	if !ok {
		return providerSpec{}, fmt.Errorf("unknown provider %q", id)
	}
	return spec, nil
}

// apiKeyFromEnv 读取内置 provider 的凭据环境变量 <PROVIDER>_API_KEY，
// 是 models.json 之外最低优先级的凭据来源
func apiKeyFromEnv(providerID string) string {
	return os.Getenv(strings.ToUpper(providerID) + "_API_KEY")
}

// Resolved 是一次 provider + 模型解析的结果。
// 调用方拿到模型元数据与对应的流函数，不需要接触 providerSpec 内部字段
type Resolved struct {
	ProviderID string
	Model      llm.Model
	Stream     llm.StreamFn
}

// Resolve 按 provider 与模型 ID 解析出可用的模型与流函数。
// modelID 为空时取该 provider 的默认模型
func (c Config) Resolve(providerID, modelID string) (Resolved, error) {
	spec, err := c.lookupProvider(providerID)
	if err != nil {
		return Resolved{}, err
	}
	models, streamFn := spec.new(spec.apiKey, spec.baseURL)
	if modelID == "" {
		modelID = spec.defaultModel
	}
	model, err := FindModel(spec.id, models, modelID)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{ProviderID: spec.id, Model: model, Stream: streamFn}, nil
}

// DefaultModelID 返回 provider 的默认模型 ID，供回退提示使用
func (c Config) DefaultModelID(providerID string) string {
	spec, err := c.lookupProvider(providerID)
	if err != nil {
		return ""
	}
	return spec.defaultModel
}

// WithStreamForTest 返回一个用指定流函数替换 provider 的副本；
// models 为 nil 时该 provider 装配会失败，用于覆盖失败分支。
// 它只服务于装配层测试的注入需求，不改变生产配置
func (c Config) WithStreamForTest(providerID string, models []llm.Model, stream llm.StreamFn) Config {
	id := strings.ToLower(strings.TrimSpace(providerID))
	spec, ok := c.registry[id]
	if !ok {
		return c
	}
	if models != nil {
		models = cloneModels(models)
	}
	spec.models = models
	spec.new = func(string, string) ([]llm.Model, llm.StreamFn) { return models, stream }
	cloned := c
	cloned.registry = make(map[string]providerSpec, len(c.registry))
	for key, value := range c.registry {
		cloned.registry[key] = value
	}
	cloned.registry[id] = spec
	return cloned
}

// NewBuiltinForTest 构造只含内置 provider 的配置，供测试装配使用
func NewBuiltinForTest(providerID, modelID string) Config {
	return Config{ProviderID: providerID, ModelID: modelID, registry: builtinRegistry()}
}

// WithProviderForTest 返回一个额外登记了自定义 provider 的副本。
// 只服务于装配层测试，不改变生产配置
func (c Config) WithProviderForTest(providerID string, models []llm.Model, stream llm.StreamFn) Config {
	cloned := c
	cloned.registry = make(map[string]providerSpec, len(c.registry)+1)
	for key, value := range c.registry {
		cloned.registry[key] = value
	}
	cloned.registry[providerID] = providerSpec{
		id:     providerID,
		apiKey: "test-key",
		models: models,
		new: func(string, string) ([]llm.Model, llm.StreamFn) {
			return cloneModels(models), stream
		},
	}
	return cloned
}
