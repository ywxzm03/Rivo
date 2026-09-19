package deepseek

import "github.com/ywxzm03/Rivo/internal/llm"

const DefaultModel = "deepseek-flash"

func builtinModels() []llm.Model {
	return []llm.Model{
		{ID: DefaultModel, Name: DefaultModel, Provider: ProviderID, API: llm.APIOpenAIResponses},
		{ID: "deepseek-v4-pro", Name: "deepseek-v4-pro", Provider: ProviderID, API: llm.APIOpenAIResponses},
	}
}
