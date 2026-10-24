package subagent

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type TaskStopTool struct {
	Store *Store
}

func (t *TaskStopTool) Name() string { return "TaskStop" }

func (t *TaskStopTool) Description() string {
	return "Stops a running background task by its ID and returns its status."
}

func (t *TaskStopTool) Parameters() map[string]any {
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

func (t *TaskStopTool) PromptSnippet() string                { return "Stop a running background task" }
func (t *TaskStopTool) PromptGuidelines() []string           { return nil }
func (t *TaskStopTool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (t *TaskStopTool) Execute(_ context.Context, args map[string]any) (llm.ToolResult, error) {
	taskID, ok := args["task_id"].(string)
	if !ok {
		taskID = ""
	}
	result, err := stopTask(t.Store, TaskStopInput{TaskID: taskID})
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: result.Message}}, Details: result}, nil
}
