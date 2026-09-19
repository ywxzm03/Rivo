package llm

type API string

const APIOpenAIResponses API = "openai-responses"

type Model struct {
	ID       string
	Name     string
	Provider string
	API      API
}
