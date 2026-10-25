package adapter

import (
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/task"
)

// 详情页的 transcript 从磁盘增量投影:首帧读全量,后续快照只消费新增部分
func TestProjectSubagentTasksReadsTranscriptIncrementally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	manager := session.NewAt("/project", path)
	if err := manager.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "on disk"}}}); err != nil {
		t.Fatal(err)
	}
	store := task.NewStore()
	if _, _, err := store.Register(task.RegisterSpec{ID: "agent-1", Type: task.TypeAgent, AgentType: "Work", OutputFile: path}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{}
	views := r.projectSubagentTasks(store, store.Snapshot(), "agent-1")
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	if len(views[0].Messages) != 1 || views[0].Messages[0].Text != "on disk" {
		t.Fatalf("projection did not read the transcript from disk: %+v", views[0].Messages)
	}
	// 1.重复快照不得重复投影同一段 transcript
	views = r.projectSubagentTasks(store, store.Snapshot(), "agent-1")
	if len(views[0].Messages) != 1 {
		t.Fatalf("repeated snapshot duplicated the transcript: %+v", views[0].Messages)
	}
	// 2.新落盘的消息只追加一次
	if err := manager.AppendMessage(llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "answer"}}}); err != nil {
		t.Fatal(err)
	}
	views = r.projectSubagentTasks(store, store.Snapshot(), "agent-1")
	if len(views[0].Messages) != 2 || views[0].Messages[1].Text != "answer" {
		t.Fatalf("incremental projection = %+v", views[0].Messages)
	}
}

// 未被查看的任务不读 transcript,内存里也不保留
func TestProjectSubagentTasksSkipsUnviewedTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	manager := session.NewAt("/project", path)
	if err := manager.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "on disk"}}}); err != nil {
		t.Fatal(err)
	}
	store := task.NewStore()
	if _, _, err := store.Register(task.RegisterSpec{ID: "agent-1", Type: task.TypeAgent, AgentType: "Work", OutputFile: path}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{}
	views := r.projectSubagentTasks(store, store.Snapshot(), "")
	if len(views) != 1 || len(views[0].Messages) != 0 {
		t.Fatalf("unviewed task projected messages: %+v", views)
	}
	if len(r.subagentTranscripts) != 0 {
		t.Fatalf("unviewed task cached a transcript: %d", len(r.subagentTranscripts))
	}
}

// 尾部 tool call 在结果落盘前先投影,结果到达后同一消息 ID 被补齐,不重复
func TestProjectSubagentTasksReProjectsPendingToolCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	manager := session.NewAt("/project", path)
	store := task.NewStore()
	if _, _, err := store.Register(task.RegisterSpec{ID: "agent-1", Type: task.TypeAgent, AgentType: "Work", OutputFile: path}); err != nil {
		t.Fatal(err)
	}
	call := llm.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"path": "a.go"}}
	if err := manager.AppendMessage(llm.AssistantMessage{Content: []llm.Content{call}}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{}
	views := r.projectSubagentTasks(store, store.Snapshot(), "agent-1")
	if len(views) != 1 || len(views[0].Messages) != 1 {
		t.Fatalf("pending projection = %+v", views)
	}
	group := views[0].Messages[0].ToolGroup
	if group == nil || len(group.Calls) != 1 || group.Calls[0].Result != "" {
		t.Fatalf("tool call before result = %+v", group)
	}
	if err := manager.AppendMessage(llm.ToolResultMessage{ToolCallID: "call-1", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "file body"}}}); err != nil {
		t.Fatal(err)
	}
	views = r.projectSubagentTasks(store, store.Snapshot(), "agent-1")
	if len(views[0].Messages) != 1 {
		t.Fatalf("result duplicated the assistant message: %+v", views[0].Messages)
	}
	group = views[0].Messages[0].ToolGroup
	if group == nil || len(group.Calls) != 1 || group.Calls[0].Result != "file body" {
		t.Fatalf("tool call after result = %+v", group)
	}
}

// 终态任务不会再补 tool result,残留尾部必须整体投影,不能永久卡在 pending
func TestProjectSubagentTasksSealsTerminalTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	manager := session.NewAt("/project", path)
	if err := manager.AppendMessage(llm.AssistantMessage{Content: []llm.Content{
		llm.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"path": "a.go"}},
	}}); err != nil {
		t.Fatal(err)
	}
	store := task.NewStore()
	id, token, err := store.Register(task.RegisterSpec{ID: "agent-1", Type: task.TypeAgent, AgentType: "Work", OutputFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(id, token, &task.AgentResult{AgentID: id}) {
		t.Fatal("completion failed")
	}
	r := &Runtime{}
	views := r.projectSubagentTasks(store, store.Snapshot(), "agent-1")
	if len(views) != 1 || len(views[0].Messages) != 1 {
		t.Fatalf("terminal projection = %+v", views)
	}
	if group := views[0].Messages[0].ToolGroup; group == nil || len(group.Calls) != 1 || group.Calls[0].ID != "call-1" {
		t.Fatalf("terminal tool group = %+v", views[0].Messages[0].ToolGroup)
	}
}
