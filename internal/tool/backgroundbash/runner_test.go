package backgroundbash

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tool/taskstoptool"
)

func TestBackgroundBashCanBeStopped(t *testing.T) {
	store := taskpkg.NewStore()
	runner := New(store)
	result, err := runner.RunBackground(context.Background(), map[string]any{
		"command":           "sleep 10",
		"run_in_background": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	details, ok := result.Details.(BackgroundOutput)
	if !ok {
		t.Fatalf("details = %#v, want task details", result.Details)
	}
	id := details.TaskID
	if id == "" {
		t.Fatalf("task ID = %#v, want non-empty", details.TaskID)
	}
	stop, err := taskstoptool.New(store).Execute(context.Background(), map[string]any{"task_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stop.Content[0].(llm.TextContent).Text, "Successfully stopped") {
		t.Fatalf("stop result = %#v", stop)
	}
	deadline := time.After(2 * time.Second)
	for {
		task, ok := store.Task(id)
		if ok && task.Base().Status == taskpkg.StatusKilled {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("task did not become killed: %#v", task)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := store.DrainNotifications(); len(got) != 0 {
		t.Fatalf("TaskStop notifications = %#v, want none", got)
	}
}
