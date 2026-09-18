package openai

import "github.com/ywxzm03/Rivo/internal/llm"

func builtinModels() []llm.Model {
	return []llm.Model{
		{ID: "gpt-5.6-sol", Name: "gpt-5.6-sol", Provider: ProviderID, API: llm.APIOpenAIResponses},
		{ID: "gpt-5.6-terra", Name: "gpt-5.6-terra", Provider: ProviderID, API: llm.APIOpenAIResponses},
		{ID: "gpt-5.6-luna", Name: "gpt-5.6-luna", Provider: ProviderID, API: llm.APIOpenAIResponses},
	}
}
