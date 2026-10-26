package taskstoptool

import (
	"context"
	"errors"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/subagent"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

func TestToolSatisfiesLLMToolContract(t *testing.T) {
	var _ llm.Tool = New(taskpkg.NewStore())
}

func TestToolMetadata(t *testing.T) {
	tool := New(taskpkg.NewStore())
	if got := tool.Name(); got != "TaskStop" {
		t.Fatalf("Name() = %q, want TaskStop", got)
	}
	if tool.Description() == "" || tool.PromptSnippet() == "" {
		t.Fatal("description or snippet is empty")
	}
	if got := tool.ExecutionMode(); got != llm.ToolExecutionParallel {
		t.Fatalf("ExecutionMode() = %q, want parallel", got)
	}
	schema := tool.Parameters()
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "task_id" {
		t.Fatalf("required = %#v, want task_id", schema["required"])
	}
}

func TestExecuteStopsAgentTask(t *testing.T) {
	store := taskpkg.NewStore()
	cancelled := make(chan struct{})
	id, _, err := store.Register(taskpkg.RegisterSpec{
		Type:        taskpkg.TypeAgent,
		Description: "long task",
		AgentType:   "Work",
		Runtime: &subagent.AgentRuntime{
			Cancel:   func() { close(cancelled) },
			DoneChan: make(chan struct{}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(store).Execute(context.Background(), map[string]any{"task_id": id})
	if err != nil {
		t.Fatal(err)
	}
	// 1.执行面必须真正取消任务,而不只是改状态
	select {
	case <-cancelled:
	default:
		t.Fatal("TaskStop did not cancel the task runtime")
	}
	// 2.任务进入 killed 终态
	task, ok := store.Task(id)
	if !ok || task.Base().Status != taskpkg.StatusKilled {
		t.Fatalf("task status = %#v, want killed", task)
	}
	if details, ok := result.Details.(taskpkg.StopOutput); !ok || details.TaskID != id {
		t.Fatalf("result details = %#v", result.Details)
	}
}

func TestExecuteRejectsUnknownTask(t *testing.T) {
	_, err := New(taskpkg.NewStore()).Execute(context.Background(), map[string]any{"task_id": "missing"})
	if err == nil {
		t.Fatal("expected an error for unknown task")
	}
	var stopErr *taskpkg.StopTaskError
	if !errors.As(err, &stopErr) || stopErr.Code != "not_found" {
		t.Fatalf("err = %v, want not_found StopTaskError", err)
	}
}

func TestExecuteRejectsMissingTaskID(t *testing.T) {
	// 缺 task_id 时必须在工具层拒绝,而不是拿空 ID 去查 store
	_, err := New(taskpkg.NewStore()).Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("missing task_id was accepted")
	}
	if _, err := New(taskpkg.NewStore()).Execute(context.Background(), map[string]any{"task_id": "   "}); err == nil {
		t.Fatal("blank task_id was accepted")
	}
}
