// Package sendmessagetool 实现 SendMessage 工具契约:向已有 subagent 任务投递输入
package sendmessagetool

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/subagent"
)

// Messenger 是工具需要的投递面,由 subagent.Manager 满足
type Messenger interface {
	SendMessage(ctx context.Context, taskID, message string) (subagent.SendMessageResult, error)
}

// Tool 是 SendMessage 工具的契约实现
type Tool struct {
	messenger Messenger
}

// New 用给定的投递面构造工具
func New(messenger Messenger) *Tool {
	return &Tool{messenger: messenger}
}

func (t *Tool) Name() string { return "SendMessage" }

func (t *Tool) Description() string {
	return "Send a message to a subagent task. A running task queues the message; a stopped task is resumed with it."
}

func (t *Tool) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"to", "message"},
		"properties": map[string]any{
			"to":      map[string]any{"type": "string", "description": "Target subagent task ID"},
			"message": map[string]any{"type": "string", "description": "Message to deliver to the subagent"},
		},
	}
}

func (t *Tool) PromptSnippet() string                { return "Send a message to a subagent task" }
func (t *Tool) PromptGuidelines() []string           { return nil }
func (t *Tool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

// Execute 把消息投递给目标任务,空字段交由执行面统一校验
func (t *Tool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	to, _ := args["to"].(string)
	message, _ := args["message"].(string)
	result, err := t.messenger.SendMessage(ctx, to, message)
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: result.Message}}, Details: result}, nil
}
