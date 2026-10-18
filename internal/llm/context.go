package llm

import "context"

type userMessageContextKey struct{}

// WithCurrentUserMessageID 把当前 tool turn 关联到已持久化的 user entry。
func WithCurrentUserMessageID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userMessageContextKey{}, id)
}

// CurrentUserMessageID 返回当前 tool turn 关联的 user entry。
func CurrentUserMessageID(ctx context.Context) string {
	id, _ := ctx.Value(userMessageContextKey{}).(string)
	return id
}

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
	Usage   *Usage
}

type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}
