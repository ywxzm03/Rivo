package openai

import "github.com/ywxzm03/Rivo/internal/llm"

const DefaultModel = "gpt-6.1-sol"

// builtinModels 返回 OpenAI 内置模型及其能力配置
func builtinModels() []llm.Model {
	return []llm.Model{
		{ID: "gpt-6-astra", Name: "gpt-6-astra", Provider: ProviderID, API: llm.APIOpenAIResponses, SupportsImages: true, Reasoning: true, ReasoningEffort: llm.ReasoningEffortMedium, ContextWindow: 1_050_000, MaxTokens: 128_000},
		{ID: DefaultModel, Name: DefaultModel, Provider: ProviderID, API: llm.APIOpenAIResponses, SupportsImages: true, Reasoning: true, ReasoningEffort: llm.ReasoningEffortMedium, ContextWindow: 1_050_000, MaxTokens: 128_000},
		{ID: "gpt-6-luna", Name: "gpt-6-luna", Provider: ProviderID, API: llm.APIOpenAIResponses, SupportsImages: true, Reasoning: true, ReasoningEffort: llm.ReasoningEffortMedium, ContextWindow: 1_050_000, MaxTokens: 128_000},
	}
}
