package task

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
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "inspect", AgentType: "Explore"})
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
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	notification := Notification{TaskID: id, Status: StatusCompleted, Summary: "done"}
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
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "inspect", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	store.EnterView(id)
	first := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}
	store.RecordAgentMessages(id, token, []llm.Message{first})

	snapshot := store.Snapshot()[0].(*AgentState)
	if len(snapshot.Messages) != 1 {
		t.Fatalf("snapshot messages = %d, want 1", len(snapshot.Messages))
	}
	// 写入方只 append:新消息不得改变已发出的 snapshot
	store.RecordAgentMessages(id, token, []llm.Message{first, llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}})
	if len(snapshot.Messages) != 1 {
		t.Fatalf("later append mutated an issued snapshot: %d messages", len(snapshot.Messages))
	}
	if got := store.Snapshot()[0].(*AgentState); len(got.Messages) != 2 {
		t.Fatalf("fresh snapshot messages = %d, want 2", len(got.Messages))
	}
}

func TestFreshStoresDoNotReuseTaskIDs(t *testing.T) {
	first := NewStore()
	second := NewStore()
	id1, _, err := first.Register(RegisterSpec{Type: TypeAgent, Description: "first", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := second.Register(RegisterSpec{Type: TypeAgent, Description: "second", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("fresh stores reused task ID %q", id1)
	}
}

func TestRegisterContinuationRejectsStaleLifecycleCallbacks(t *testing.T) {
	store := NewStore()
	id, oldToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(id, oldToken, &AgentResult{AgentID: id}) {
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
	if store.CompleteAgent(id, oldToken, &AgentResult{AgentID: id}) {
		t.Fatal("stale completion changed the replacement lifecycle")
	}
	if !store.CompleteAgent(id, newToken, &AgentResult{AgentID: id}) {
		t.Fatal("replacement completion was rejected")
	}
}

func TestAgentTaskStopLeavesNotificationForPartialResult(t *testing.T) {
	store := NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	id, token, err := store.Register(RegisterSpec{
		Type:        TypeAgent,
		Description: "long task",
		AgentType:   "Work",
		Runtime:     &AgentRuntime{Cancel: cancel, Done: make(chan struct{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := KillAgent(id, store); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("TaskStop did not cancel agent context")
	}
	task, _ := store.Task(id)
	if task.Base().Status != StatusKilled || task.Base().Notified {
		t.Fatalf("task after stop = %#v, want killed and notification eligible", task.Base())
	}
	if !store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusKilled}) {
		t.Fatal("Agent TaskStop should retain terminal notification")
	}
}

func TestContinuationWaitsForOldRuntime(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	done := make(chan struct{})
	runtime := &AgentRuntime{Done: done}
	store.mu.Lock()
	store.runtimes[id] = runtime
	store.mu.Unlock()
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(done)
	}()
	old, _, err := store.ContinuationRuntime(id)
	if err != nil || old != runtime {
		t.Fatalf("continuation runtime = %p, %v; want %p", old, err, runtime)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old runtime did not finish")
	}
}

func TestStoreEnterViewLoadsTranscriptOnce(t *testing.T) {
	store := NewStore()
	id, _, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "view", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	// 没有 OutputFile 时 EnterView 仍应成功,只是没有可加载的 transcript
	if !store.EnterView(id) {
		t.Fatal("EnterView rejected a registered task")
	}
	if got := store.ViewingTaskID(); got != id {
		t.Fatalf("viewing task = %q, want %q", got, id)
	}
	store.ExitView()
	if got := store.ViewingTaskID(); got != "" {
		t.Fatalf("viewing task after exit = %q, want empty", got)
	}
}

func TestStoreEvictDropsTerminalTaskAndNotifications(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "evict", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	// 1.运行中的任务不可淘汰
	if store.Evict(id) {
		t.Fatal("running task must not be evicted")
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted})
	// 2.终态任务淘汰后,任务和通知都从根投影消失
	if !store.Evict(id) {
		t.Fatal("terminal task was not evicted")
	}
	if _, ok := store.Task(id); ok {
		t.Fatal("evicted task is still tracked")
	}
	if got := store.DrainNotifications(); len(got) != 0 {
		t.Fatalf("notifications after evict = %#v, want empty", got)
	}
}
