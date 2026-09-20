package llm

import "context"

type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) (string, error)
}

type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}
