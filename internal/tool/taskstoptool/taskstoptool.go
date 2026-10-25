// Package taskstoptool 实现 TaskStop 工具契约:按 ID 终止后台任务
package taskstoptool

import (
	"context"
	"errors"
	"strings"

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

// Execute 终止目标任务,缺少 task_id 时返回明确错误
func (t *Tool) Execute(_ context.Context, args map[string]any) (llm.ToolResult, error) {
	if t.store == nil {
		return llm.ToolResult{}, errMissingStore
	}
	taskID, ok := args["task_id"].(string)
	taskID = strings.TrimSpace(taskID)
	if !ok || taskID == "" {
		return llm.ToolResult{}, errMissingTaskID
	}
	result, err := taskpkg.StopTask(t.store, taskpkg.StopInput{TaskID: taskID})
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: result.Message}}, Details: result}, nil
}

var (
	errMissingStore  = errors.New("TaskStop: task store is unavailable")
	errMissingTaskID = errors.New("TaskStop: task_id is required")
)
