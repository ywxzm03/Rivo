package llm

type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}
