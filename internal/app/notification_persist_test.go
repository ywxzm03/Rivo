package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// 通知落盘后进入主会话,模型后续轮次仍能回看
func TestNotificationPersistsIntoParentSession(t *testing.T) {
	sm := session.New("/work", t.TempDir())
	coord := NewCoordinator(sm, nil)
	notification := llm.UserMessage{Content: []llm.Content{
		llm.TextContent{Text: "<task-notification>gin-demo done</task-notification>"},
	}}
	acked := 0
	coord.SetNotificationClaimer(func() ([]llm.Message, func()) {
		if acked > 0 {
			return nil, nil
		}
		return []llm.Message{notification}, func() { acked++ }
	})

	// 第一次准备请求:认领器可用
	request, err := coord.PrepareRequest(context.Background(), agent.PreparedRequest{
		Context: llm.Context{Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "input"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, ack := request.ClaimNotifications()
	if len(messages) != 1 {
		t.Fatalf("claimed = %#v, want one notification", messages)
	}

	// 模拟 agent 落盘后再 ack
	if err := sm.AppendMessage(messages[0]); err != nil {
		t.Fatal(err)
	}
	ack()

	if acked != 1 {
		t.Fatalf("ack count = %d, want 1", acked)
	}
	persisted := sm.BuildContext().Messages()
	found := false
	for _, message := range persisted {
		if user, ok := message.(llm.UserMessage); ok {
			for _, content := range user.Content {
				if text, ok := content.(llm.TextContent); ok && strings.Contains(text.Text, "gin-demo done") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("persisted messages = %#v, want the notification to survive in the session", persisted)
	}
}

// ack 之后认领器不再返回同一条通知,避免重复落盘
func TestNotificationClaimedOnce(t *testing.T) {
	sm := session.New("/work", t.TempDir())
	coord := NewCoordinator(sm, nil)
	remaining := []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "<task-notification>x</task-notification>"}}}}
	coord.SetNotificationClaimer(func() ([]llm.Message, func()) {
		if len(remaining) == 0 {
			return nil, nil
		}
		batch := remaining
		return batch, func() { remaining = nil }
	})

	request, err := coord.PrepareRequest(context.Background(), agent.PreparedRequest{})
	if err != nil {
		t.Fatal(err)
	}
	first, ack := request.ClaimNotifications()
	if len(first) != 1 {
		t.Fatalf("first claim = %#v, want one", first)
	}
	ack()

	second, _ := request.ClaimNotifications()
	if len(second) != 0 {
		t.Fatalf("second claim = %#v, want none after ack", second)
	}
}
