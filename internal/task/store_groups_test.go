package task

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 子结构必须共享同一把锁与同一份状态：通过 Views 进入视图后，
// Store 侧看到的是同一个 viewingTaskID
func TestViewsSharesStateWithStore(t *testing.T) {
	store := NewStore()
	id, _, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.Views.Enter(id) {
		t.Fatal("Views.Enter() = false, want true for a registered agent task")
	}
	if got := store.ViewingTaskID(); got != id {
		t.Fatalf("Store.ViewingTaskID() = %q, want %q", got, id)
	}
	if got := store.Views.Viewing(); got != id {
		t.Fatalf("Views.Viewing() = %q, want %q", got, id)
	}
	store.Views.Exit()
	if got := store.ViewingTaskID(); got != "" {
		t.Fatalf("Store.ViewingTaskID() = %q after exit, want empty", got)
	}
}

// 通知队列必须与 Store 的既有入口共享同一份数据
func TestNotificationsSharesQueueWithStore(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "job"})
	if err != nil {
		t.Fatal(err)
	}
	// 通知只对终态任务入队
	if !store.CompleteAgent(id, token, nil) {
		t.Fatal("task did not transition to completed")
	}
	if !store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted, Summary: "job"}) {
		t.Fatal("EnqueueNotification() = false, want true")
	}
	if got := len(store.Notifications.Peek()); got != 1 {
		t.Fatalf("Notifications.Peek() = %d, want 1", got)
	}
	drained := store.Notifications.Drain()
	if len(drained) != 1 {
		t.Fatalf("Notifications.Drain() = %d, want 1", len(drained))
	}
	if got := len(store.PeekNotifications()); got != 0 {
		t.Fatalf("Store.PeekNotifications() = %d after drain, want 0", got)
	}
}

// 待注入队列按任务与种类隔离，子结构入口与 Store 入口等价
func TestPendingQueuesShareStateWithStore(t *testing.T) {
	store := NewStore()
	id, _, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Pending.Enqueue(id, false, testMessage("steer")); err != nil {
		t.Fatalf("Pending.Enqueue() error = %v", err)
	}
	if got := store.PendingMessages(id); len(got) != 1 {
		t.Fatalf("Store.PendingMessages() = %d, want 1", len(got))
	}
	if err := store.Pending.Enqueue(id, true, testMessage("follow")); err != nil {
		t.Fatalf("Pending.Enqueue(follow-up) error = %v", err)
	}
	if got := len(store.PendingFollowUpMessages(id)); got != 1 {
		t.Fatalf("Store.PendingFollowUpMessages() = %d, want 1", got)
	}
	if got := store.Pending.Drain(id, false); len(got) != 1 {
		t.Fatalf("Pending.Drain(steering) = %d, want 1", len(got))
	}
	if got := len(store.PendingMessages(id)); got != 0 {
		t.Fatalf("Store.PendingMessages() = %d after drain, want 0", got)
	}
	if got := len(store.PendingFollowUpMessages(id)); got != 1 {
		t.Fatalf("follow-up queue leaked: %d, want 1", got)
	}
}

func testMessage(text string) llm.UserMessage {
	return llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}}
}
