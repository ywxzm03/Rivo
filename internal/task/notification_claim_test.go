package task

import (
	"testing"
	"time"
)

// newTerminalTask 注册一个任务并把它推进到终态
func newTerminalTask(t *testing.T, store *Store, description string) (string, uint64) {
	t.Helper()
	id, token, err := store.Register(RegisterSpec{
		Type:        TypeAgent,
		Description: description,
		AgentState:  &AgentState{AgentType: "Work", Prompt: "go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{Content: []TextBlock{{Text: "done"}}})
	return id, token
}

// 入队只置去重标记，送达标记要等 ack
func TestEnqueueNotificationKeepsDeliveredFalse(t *testing.T) {
	store := NewStore()
	id, token := newTerminalTask(t, store, "job")
	if !store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted, Summary: "done"}) {
		t.Fatal("enqueue rejected")
	}
	state, ok := store.Task(id)
	if !ok {
		t.Fatal("task missing")
	}
	if state.Base().NotificationDelivered {
		t.Fatal("NotificationDelivered set at enqueue time, want it set only after ack")
	}
}

// 认领不移除队列，落盘失败时可以重来
func TestClaimNotificationsKeepsQueue(t *testing.T) {
	store := NewStore()
	id, token := newTerminalTask(t, store, "job")
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted, Summary: "done"})

	first := store.ClaimNotifications()
	second := store.ClaimNotifications()
	if first.Count != 1 || second.Count != 1 {
		t.Fatalf("claim counts = %d, %d, want 1, 1", first.Count, second.Count)
	}
	if first.Notifications[0].TaskID != second.Notifications[0].TaskID {
		t.Fatal("second claim returned a different notification")
	}
}

// ack 之后才真正出队
func TestAckNotificationsAdvances(t *testing.T) {
	store := NewStore()
	id, token := newTerminalTask(t, store, "job")
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted, Summary: "done"})

	claimed := store.ClaimNotifications()
	store.AckNotifications(claimed.Count)

	if got := store.ClaimNotifications(); got.Count != 0 {
		t.Fatalf("claim after ack = %d, want 0", got.Count)
	}
}

// ack 之后 Notified 才置位，驱逐也要等它
func TestMarkNotifiedAfterAck(t *testing.T) {
	store := NewStore()
	id, token := newTerminalTask(t, store, "job")
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted, Summary: "done"})

	claimed := store.ClaimNotifications()
	store.AckNotifications(claimed.Count)
	store.MarkNotified([]string{id})

	state, _ := store.Task(id)
	if !state.Base().NotificationDelivered {
		t.Fatal("NotificationDelivered still false after ack, want true")
	}
}

// 未 ack 的终态任务不能被驱逐，否则通知还没送达任务行就消失了
func TestEvictExpiredWaitsForNotified(t *testing.T) {
	store := NewStore()
	id, token := newTerminalTask(t, store, "job")
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted, Summary: "done"})

	// 把宽限期压到过去，模拟已过期
	state, _ := store.Task(id)
	past := time.Now().Add(-time.Hour)
	state.Base().EvictAfter = &past

	if evicted := store.EvictExpired(); evicted != 0 {
		t.Fatalf("evicted %d tasks before the notification was acked, want 0", evicted)
	}
	if _, ok := store.Task(id); !ok {
		t.Fatal("task evicted before its notification was acked")
	}
}
