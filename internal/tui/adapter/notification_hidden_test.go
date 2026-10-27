package adapter

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// notificationMessage 构造一条与 app 层一致的隐藏通知消息
func notificationMessage() llm.UserMessage {
	return llm.UserMessage{Hidden: true, Content: []llm.Content{llm.TextContent{
		Text: "<task-notification>\n<task-id>bash-1</task-id>\n<status>completed</status>\n<summary>background bash completed</summary>\n</task-notification>",
	}}}
}

// 通知在 live 投影里不能变成转录消息
func TestHiddenNotificationNotProjectedLive(t *testing.T) {
	runtime := New(provider.Config{}, "/project", "main")
	runtime.namespace = "session"
	if err := runtime.handleEvent(llm.MessageEndEvent{Message: notificationMessage(), SourceID: "entry-1"}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.messages) != 0 {
		t.Fatalf("messages = %+v, want the hidden notification kept out of the transcript", runtime.messages)
	}
}

// 会话回放同样不能把历史里的通知渲染出来
func TestHiddenNotificationNotProjectedOnReplay(t *testing.T) {
	runtime := New(provider.Config{}, "/project", "main")
	view := ctxview.ContextView{Entries: []ctxview.ContextEntry{
		{SourceID: "user-1", Kind: ctxview.ContextMessage, Messages: []llm.Message{
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "run the tests"}}},
		}},
		{SourceID: "notice-1", Kind: ctxview.ContextMessage, Messages: []llm.Message{notificationMessage()}},
		{SourceID: "user-2", Kind: ctxview.ContextMessage, Messages: []llm.Message{
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "thanks"}}},
		}},
	}}

	projected := runtime.projectContext(view)
	if len(projected) != 2 {
		t.Fatalf("projected = %+v, want only the two user messages", projected)
	}
	if projected[0].Text != "run the tests" || projected[1].Text != "thanks" {
		t.Fatalf("projected = %+v, want visible messages untouched", projected)
	}
	for _, message := range projected {
		if strings.Contains(message.Text, "task-notification") {
			t.Fatalf("hidden notification leaked into projection: %+v", message)
		}
	}
}

// 跳过渲染不能连带破坏工具组收尾:通知落在两个 assistant 轮次之间时仍要封组
func TestHiddenNotificationSealsToolGroup(t *testing.T) {
	runtime := New(provider.Config{}, "/project", "main")
	view := ctxview.ContextView{Entries: []ctxview.ContextEntry{
		{SourceID: "assistant-1", Kind: ctxview.ContextMessage, Messages: []llm.Message{
			llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "call-1", Name: "bash"}}, StopReason: llm.StopReasonToolUse},
		}},
		{SourceID: "notice-1", Kind: ctxview.ContextMessage, Messages: []llm.Message{notificationMessage()}},
		{SourceID: "assistant-2", Kind: ctxview.ContextMessage, Messages: []llm.Message{
			llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}},
		}},
	}}

	projected := runtime.projectContext(view)
	if len(projected) != 2 {
		t.Fatalf("projected = %+v, want the tool turn and the answer", projected)
	}
	if projected[0].ToolGroup == nil || len(projected[0].ToolGroup.Calls) != 1 {
		t.Fatalf("tool group = %+v, want one call", projected[0].ToolGroup)
	}
	if status := projected[0].ToolGroup.Calls[0].Status; status != tui.ToolInterrupted {
		t.Fatalf("call status = %q, want the group sealed at the notification boundary", status)
	}
}

// 会话树只列用户可回退的 user 节点,通知不能成为其中一个节点
func TestProjectTreeSkipsHiddenNotificationEntries(t *testing.T) {
	snapshot := session.TreeSnapshot{
		LeafID: "assistant-2",
		Entries: []session.TreeEntry{
			{ID: "user-1", Role: "user", Text: "run the tests"},
			{ID: "notice-1", ParentID: "user-1", Role: "user", Text: "<task-notification>", Hidden: true},
			{ID: "assistant-2", ParentID: "notice-1", Role: "assistant", Text: "done"},
		},
	}

	nodes := projectTree(snapshot)
	if len(nodes) != 1 || nodes[0].ID != "user-1" {
		t.Fatalf("nodes = %+v, want only the visible user node", nodes)
	}
	if !nodes[0].Current {
		t.Fatalf("current node = %+v, want the leaf resolved through the hidden entry", nodes[0])
	}
}

// 端到端:通知经 session 落盘后模型上下文里仍有它,而 TUI 投影一条都不产出
// 两个方向同时断言,防止修显示时把投递能力一起改坏
func TestPersistedNotificationReachesModelButNotTranscript(t *testing.T) {
	manager := session.New("/project", t.TempDir())
	notification := notificationMessage()
	if _, err := manager.AppendMessages([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "run the tests"}}},
		notification,
	}); err != nil {
		t.Fatal(err)
	}
	// 重开一次会话,覆盖"落盘后重新读盘"这条真实路径
	reopened, err := session.Open(manager.Path(), "/project")
	if err != nil {
		t.Fatal(err)
	}
	view := reopened.BuildContext()

	delivered := false
	for _, message := range view.Messages() {
		if user, ok := message.(llm.UserMessage); ok && strings.Contains(llm.TextOf(user.Content), "background bash completed") {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("notification disappeared from the model context, the agent would never learn the bash task finished")
	}

	runtime := New(provider.Config{}, "/project", "main")
	projected := runtime.projectContext(view)
	if len(projected) != 1 || projected[0].Text != "run the tests" {
		t.Fatalf("projected = %+v, want only the user prompt", projected)
	}
}
