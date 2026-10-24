package subagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestStorePendingMessagesFIFOAndNotificationDeduplication(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TaskTypeAgent, Description: "inspect", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	first := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}
	second := llm.UserMessage{Content: []llm.Content{llm.ImageContent{Data: "image", MIMEType: "image/png"}}}
	if err := store.EnqueuePendingMessage(id, first); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueuePendingMessage(id, second); err != nil {
		t.Fatal(err)
	}
	messages := store.DrainPendingMessages(id, token)
	if len(messages) != 2 || messages[0].Content[0].(llm.TextContent).Text != "first" {
		t.Fatalf("pending messages = %#v, want FIFO batch", messages)
	}
	if got := store.DrainPendingMessages(id, token); got != nil {
		t.Fatalf("second drain = %#v, want empty", got)
	}
	store.CompleteAgent(id, token, &AgentToolResult{AgentID: id})
	notification := TaskNotification{TaskID: id, Status: StateCompleted, Summary: "done"}
	if !store.EnqueueNotification(id, token, notification) || store.EnqueueNotification(id, token, notification) {
		t.Fatal("notification should be enqueued once")
	}
	got := store.DrainNotifications()
	if len(got) != 1 || got[0].TaskID != id {
		t.Fatalf("notifications = %#v, want one notification", got)
	}
}

func TestSnapshotSharesImmutableTranscriptPrefix(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TaskTypeAgent, Description: "inspect", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	store.EnterView(id)
	first := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}
	store.RecordAgentMessages(id, token, []llm.Message{first})

	snapshot := store.Snapshot()[0].(*AgentTaskState)
	if len(snapshot.Messages) != 1 {
		t.Fatalf("snapshot messages = %d, want 1", len(snapshot.Messages))
	}
	// 写入方只 append:新消息不得改变已发出的 snapshot
	store.RecordAgentMessages(id, token, []llm.Message{first, llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}})
	if len(snapshot.Messages) != 1 {
		t.Fatalf("later append mutated an issued snapshot: %d messages", len(snapshot.Messages))
	}
	if got := store.Snapshot()[0].(*AgentTaskState); len(got.Messages) != 2 {
		t.Fatalf("fresh snapshot messages = %d, want 2", len(got.Messages))
	}
}

func TestFreshStoresDoNotReuseTaskIDs(t *testing.T) {
	first := NewStore()
	second := NewStore()
	id1, _, err := first.Register(RegisterSpec{Type: TaskTypeAgent, Description: "first", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := second.Register(RegisterSpec{Type: TaskTypeAgent, Description: "second", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("fresh stores reused task ID %q", id1)
	}
}

func TestRegisterContinuationRejectsStaleLifecycleCallbacks(t *testing.T) {
	store := NewStore()
	id, oldToken, err := store.Register(RegisterSpec{Type: TaskTypeAgent, Description: "work", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(id, oldToken, &AgentToolResult{AgentID: id}) {
		t.Fatal("initial completion failed")
	}
	runtime := &AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
	newToken, err := store.RegisterContinuation(id, runtime, "continue")
	if err != nil {
		t.Fatal(err)
	}
	if newToken == oldToken {
		t.Fatal("continuation reused lifecycle token")
	}
	if store.CompleteAgent(id, oldToken, &AgentToolResult{AgentID: id}) {
		t.Fatal("stale completion changed the replacement lifecycle")
	}
	if !store.CompleteAgent(id, newToken, &AgentToolResult{AgentID: id}) {
		t.Fatal("replacement completion was rejected")
	}
}

func TestAgentTaskStopLeavesNotificationForPartialResult(t *testing.T) {
	store := NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	id, token, err := store.Register(RegisterSpec{
		Type:        TaskTypeAgent,
		Description: "long task",
		AgentType:   "Work",
		Runtime:     &AgentRuntime{Cancel: cancel, Done: make(chan struct{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (AgentTask{}).Kill(id, store); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("TaskStop did not cancel agent context")
	}
	task, _ := store.Task(id)
	if task.Base().Status != StateKilled || task.Base().Notified {
		t.Fatalf("task after stop = %#v, want killed and notification eligible", task.Base())
	}
	if !store.EnqueueNotification(id, token, TaskNotification{TaskID: id, Status: StateKilled}) {
		t.Fatal("Agent TaskStop should retain terminal notification")
	}
}

func TestPendingDrainInstallsSteeringMessages(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TaskTypeAgent, Description: "work", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	child := agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil)
	runtime := &AgentRuntime{Agent: child, Done: make(chan struct{})}
	manager := NewManager(store, nil)
	if _, err := store.attachAgentRuntime(id, runtime, "work"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueuePendingMessage(id, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "queued"}}}); err != nil {
		t.Fatal(err)
	}
	manager.installPendingDrain(id, token, runtime)
	if err := runtime.Agent.OnEvent(llm.TurnEndEvent{}); err != nil {
		t.Fatal(err)
	}
	if got := store.PendingMessages(id); len(got) != 0 {
		t.Fatalf("pending queue after drain = %#v, want empty", got)
	}
	if !child.HasSteering() {
		t.Fatal("drained message was not handed to agent steering")
	}
}

func TestContinuationWaitsForOldRuntime(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TaskTypeAgent, Description: "work", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentToolResult{AgentID: id})
	done := make(chan struct{})
	runtime := &AgentRuntime{Done: done}
	store.mu.Lock()
	store.runtimes[id] = runtime
	store.mu.Unlock()
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(done)
	}()
	old, _, err := store.continuationRuntime(id)
	if err != nil || old != runtime {
		t.Fatalf("continuation runtime = %p, %v; want %p", old, err, runtime)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old runtime did not finish")
	}
}
