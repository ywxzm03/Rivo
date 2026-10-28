package app

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// 通知消息必须带隐藏标记:模型照常读到正文,TUI 据此跳过渲染
func TestNotificationMessagesAreHidden(t *testing.T) {
	messages := subagentNotificationMessages([]taskpkg.Notification{{
		TaskID:     "bash-179311620747996000-2",
		Status:     taskpkg.StatusCompleted,
		OutputFile: "/tmp/rivo-bash.log",
		Summary:    "background bash completed",
	}})
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	user, ok := messages[0].(llm.UserMessage)
	if !ok {
		t.Fatalf("message type = %T, want llm.UserMessage", messages[0])
	}
	if !user.Hidden {
		t.Fatal("notification message is not marked hidden, so the TUI would render it")
	}
	// 隐藏只影响显示,正文必须原样保留给模型
	text := llm.TextOf(user.Content)
	for _, want := range []string{"<task-notification>", "<task-id>bash-179311620747996000-2</task-id>", "background bash completed", "</task-notification>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

// 通知落盘不该顺带建文件历史快照:它不是一轮对话的起点
func TestNotificationCommitSkipsFileHistorySnapshot(t *testing.T) {
	sm := session.New("/work", t.TempDir())
	sm.AttachFileHistory(t.TempDir())
	coord := NewCoordinator(sm, nil)

	ids, err := coord.CommitMessages(subagentNotificationMessages([]taskpkg.Notification{{
		TaskID:  "bash-1",
		Status:  taskpkg.StatusCompleted,
		Summary: "background bash completed",
	}}))
	if err != nil {
		t.Fatalf("CommitMessages() error = %v", err)
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("ids = %v, want the notification persisted", ids)
	}
	if snapshots := sm.FileHistory().Snapshots(); len(snapshots) != 0 {
		t.Fatalf("snapshots = %+v, want none for a notification", snapshots)
	}
}
