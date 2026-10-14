package llm

import "context"

type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	// PromptSnippet 返回 tools 分节中的一行说明；无贡献时返回空串
	PromptSnippet() string
	// PromptGuidelines 返回 rules 分节中的条目；无贡献时返回 nil
	PromptGuidelines() []string
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
