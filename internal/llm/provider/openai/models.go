package openai

import "github.com/ywxzm03/Rivo/internal/llm"

const DefaultModel = "gpt-5.6-sol"

func builtinModels() []llm.Model {
	return []llm.Model{
		{ID: DefaultModel, Name: DefaultModel, Provider: ProviderID, API: llm.APIOpenAIResponses, SupportsImages: true, Reasoning: true, ReasoningEffort: llm.ReasoningEffortMedium},
		{ID: "gpt-5.6-terra", Name: "gpt-5.6-terra", Provider: ProviderID, API: llm.APIOpenAIResponses, SupportsImages: true, Reasoning: true, ReasoningEffort: llm.ReasoningEffortMedium},
		{ID: "gpt-5.6-luna", Name: "gpt-5.6-luna", Provider: ProviderID, API: llm.APIOpenAIResponses, SupportsImages: true, Reasoning: true, ReasoningEffort: llm.ReasoningEffortMedium},
	}
}
