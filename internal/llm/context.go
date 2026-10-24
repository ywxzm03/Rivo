package llm

import "context"

type userMessageContextKey struct{}

type toolContextKey struct{}
type workingDirKey struct{}

// ToolContext carries the executing agent's context to tools that need to spawn
// or coordinate child work without exposing the Agent implementation.
type ToolContext struct {
	Context Context
	Config  AgentConfig
}

func WithToolContext(ctx context.Context, toolContext ToolContext) context.Context {
	return context.WithValue(ctx, toolContextKey{}, toolContext)
}

func CurrentToolContext(ctx context.Context) (ToolContext, bool) {
	value, ok := ctx.Value(toolContextKey{}).(ToolContext)
	return value, ok
}

func WithWorkingDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, workingDirKey{}, dir)
}

func CurrentWorkingDir(ctx context.Context) string {
	dir, _ := ctx.Value(workingDirKey{}).(string)
	return dir
}

// WithCurrentUserMessageID 把当前 tool turn 关联到已持久化的 user entry
func WithCurrentUserMessageID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userMessageContextKey{}, id)
}

func CurrentUserMessageID(ctx context.Context) string {
	id, _ := ctx.Value(userMessageContextKey{}).(string)
	return id
}

type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	// PromptSnippet 返回 tools 分节中的一行说明，无贡献时返回空串
	PromptSnippet() string
	// PromptGuidelines 返回 rules 分节中的条目，无贡献时返回 nil
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
