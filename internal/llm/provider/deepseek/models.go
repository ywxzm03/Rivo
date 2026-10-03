package deepseek

import "github.com/ywxzm03/Rivo/internal/llm"

const DefaultModel = "deepseek-flash"

// builtinModels 返回 DeepSeek 内置模型及其能力配置
func builtinModels() []llm.Model {
	return []llm.Model{
		{ID: DefaultModel, Name: DefaultModel, Provider: ProviderID, API: llm.APIOpenAIResponses, SupportsImages: true, Reasoning: true, ReasoningEffort: llm.ReasoningEffortMedium, ContextWindow: 128000, MaxTokens: 16384},
		{ID: "deepseek-v4-pro", Name: "deepseek-v4-pro", Provider: ProviderID, API: llm.APIOpenAIResponses, SupportsImages: true, Reasoning: true, ReasoningEffort: llm.ReasoningEffortMedium, ContextWindow: 128000, MaxTokens: 16384},
	}
}
