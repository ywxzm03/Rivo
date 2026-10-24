package adapter

import (
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/task"
)

func TestProjectSubagentTasksUsesLoadedTranscriptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	manager := session.NewAt("/project", path)
	if err := manager.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "on disk"}}}); err != nil {
		t.Fatal(err)
	}
	state := &task.AgentState{
		StateBase: task.StateBase{
			ID:         "agent-1",
			Type:       task.TypeAgent,
			Status:     task.StatusRunning,
			OutputFile: path,
		},
		DiskLoaded: true,
	}
	views := projectSubagentTasks([]task.State{state}, "agent-1")
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	if len(views[0].Messages) != 0 {
		t.Fatalf("projection reloaded transcript after Store load: %+v", views[0].Messages)
	}
}
