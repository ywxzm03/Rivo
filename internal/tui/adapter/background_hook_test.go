package adapter

import (
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// 会话切换后,新 App 的通知唤醒与后台结束兜底都必须照常工作
func TestNotificationPathSurvivesSessionSwitch(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, first.Cwd(), "")
	r.Attach(first)

	var mu sync.Mutex
	var sent []tea.Msg
	r.mu.Lock()
	r.send = func(msg tea.Msg) {
		mu.Lock()
		sent = append(sent, msg)
		mu.Unlock()
	}
	r.mu.Unlock()

	done := r.request(tui.Request{Kind: "new"})().(tui.OperationDoneMsg)
	if done.Err != nil {
		t.Fatalf("new session failed: %v", done.Err)
	}
	second := r.app
	if second == first {
		t.Fatal("session did not switch to a new app")
	}

	// 在新会话里投递一条终态通知,后台 run 结束后必须发出忙碌态复位
	store := second.SubagentStore()
	if store == nil {
		t.Fatal("subagent store is unavailable")
	}
	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "job", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, &taskpkg.AgentResult{AgentID: taskID}) {
		t.Fatal("task did not transition to completed")
	}
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "job"}) {
		t.Fatal("notification was not enqueued")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, msg := range sent {
			if end, ok := msg.(tui.BackgroundRunMsg); ok && !end.Running {
				mu.Unlock()
				return
			}
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("no background end message after session switch: %#v", sent)
}
