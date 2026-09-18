package llm

type API string

const (
	APIOpenAIResponses   API = "openai-responses"
	APIOpenAICompletions API = "openai-completions"
)

type Model struct {
	ID       string
	Name     string
	Provider string
	API      API
}
