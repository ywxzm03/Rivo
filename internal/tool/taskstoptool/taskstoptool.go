// Package taskstoptool 实现 TaskStop 工具契约:按 ID 终止后台任务
package taskstoptool

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// Tool 是 TaskStop 工具的契约实现
type Tool struct {
	store *taskpkg.Store
}

// New 用根任务 store 构造工具
func New(store *taskpkg.Store) *Tool {
	return &Tool{store: store}
}

func (t *Tool) Name() string { return "TaskStop" }

func (t *Tool) Description() string {
	return "Stops a running background task by its ID and returns its status."
}

func (t *Tool) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"task_id"},
		"properties": map[string]any{
			"task_id": map[string]any{
				"type":        "string",
				"description": "The ID of the background task to stop",
			},
		},
	}
}

func (t *Tool) PromptSnippet() string                { return "Stop a running background task" }
func (t *Tool) PromptGuidelines() []string           { return nil }
func (t *Tool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

// Execute 终止目标任务;缺少 task_id 时按空 ID 处理,由执行面报 not_found
func (t *Tool) Execute(_ context.Context, args map[string]any) (llm.ToolResult, error) {
	taskID, ok := args["task_id"].(string)
	if !ok {
		taskID = ""
	}
	result, err := taskpkg.StopTask(t.store, taskpkg.StopInput{TaskID: taskID})
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: result.Message}}, Details: result}, nil
}
