package llm

import "context"

type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	ExecutionMode() ToolExecutionMode
	Execute(ctx context.Context, args map[string]any) (ToolResult, error)
}

type ToolExecutionMode string

const (
	ToolExecutionParallel   ToolExecutionMode = "parallel"
	ToolExecutionSequential ToolExecutionMode = "sequential"
)

type ToolResult struct {
	Content []Content
	Details any
	IsError bool
}

type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}
