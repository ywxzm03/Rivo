package llm

type API string

const APIOpenAIResponses API = "openai-responses"

type Model struct {
	ID       string
	Name     string
	Provider string
	API      API

	// SupportsImages 表示这个模型接受图片输入
	SupportsImages bool
}
