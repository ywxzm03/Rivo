package app

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// panelGraceWait 覆盖 panelGracePeriod 加上驱逐 tick 的抖动
const panelGraceWait = 45 * time.Second

// evictOnlyApp 返回一个不会自动消费通知的 App,供驱逐测试单独观察 store
func evictOnlyApp(t *testing.T) *App {
	t.Helper()
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// 用户不再发任何消息时,终态任务也必须被定时器驱逐,不能等下一次 PrepareRequest
func TestEvictionLoopEvictsWithoutUserInput(t *testing.T) {
	a := evictOnlyApp(t)
	defer a.ShutdownSubagents()
	store := a.SubagentStore()
	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "idle job", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, &taskpkg.AgentResult{AgentID: taskID}) {
		t.Fatal("task did not transition to completed")
	}
	// 通知先入队,任务才具备驱逐资格
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "idle job"}) {
		t.Fatal("notification was not enqueued")
	}
	// 不调用 Start、不发消息,只等定时器把宽限期走完
	deadline := time.Now().Add(panelGraceWait)
	for time.Now().Before(deadline) {
		if _, ok := store.Task(taskID); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("terminal task survived %v without any user input", panelGraceWait)
}

// 主 agent 正在跑多轮工具循环时,子任务终态通知必须进入后续轮次的请求上下文,
// 不能等到整个 operation 收尾
func TestNotificationInjectedDuringActiveRun(t *testing.T) {
	a := evictOnlyApp(t)
	defer a.ShutdownSubagents()
	var mu sync.Mutex
	var sawNotification bool
	rounds := 0
	a.agent.StreamFn = func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		for _, message := range c.Messages {
			user, ok := message.(llm.UserMessage)
			if !ok {
				continue
			}
			for _, content := range user.Content {
				if text, ok := content.(llm.TextContent); ok && strings.Contains(text.Text, "<task-notification>") {
					mu.Lock()
					sawNotification = true
					mu.Unlock()
				}
			}
		}
		mu.Lock()
		rounds++
		current := rounds
		mu.Unlock()
		events := make(chan llm.AssistantMessageEvent, 1)
		// 第一轮发起工具调用,把 operation 拉进多轮循环
		if current == 1 {
			events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{
				llm.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"path": "/nonexistent"}},
			}}}
		} else {
			events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}}}
		}
		close(events)
		return events, nil
	}
	store := a.SubagentStore()
	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "mid run", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, &taskpkg.AgentResult{AgentID: taskID}) {
		t.Fatal("task did not transition to completed")
	}
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "mid run"}) {
		t.Fatal("notification was not enqueued")
	}
	if _, err := a.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawNotification {
		t.Fatalf("notification missing from request context across %d rounds", rounds)
	}
}

// Start 被运行中的 operation 拒绝时,通知信号必须重新入队而不是丢弃
func TestStartRejectedRetriesNotification(t *testing.T) {
	a := evictOnlyApp(t)
	defer a.ShutdownSubagents()
	block := make(chan struct{})
	var calls atomic.Int64
	a.agent.StreamFn = func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		if calls.Add(1) == 1 {
			<-block
		}
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}}}
		close(events)
		return events, nil
	}
	store := a.SubagentStore()
	// 1.占住 operation 槽位
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "busy"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	// 2.忙碌期间投递通知,此时通知循环的 Start 必然被拒
	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "queued", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, &taskpkg.AgentResult{AgentID: taskID}) {
		t.Fatal("task did not transition to completed")
	}
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "queued"}) {
		t.Fatal("notification was not enqueued")
	}
	time.Sleep(150 * time.Millisecond)
	// 3.释放槽位,通知必须被消费
	close(block)
	op.Wait()
	deadline := time.Now().Add(panelGraceWait)
	for time.Now().Before(deadline) {
		if len(store.PeekNotifications()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("notification stranded after busy window: pending=%d", len(store.PeekNotifications()))
}

// 通知必须渲染成 CC 风格的 XML 标签,而不是 JSON payload
func TestNotificationXMLShape(t *testing.T) {
	messages := subagentNotificationMessages([]taskpkg.Notification{{
		TaskID:       "agent-1",
		ToolUseID:    "call-9",
		Status:       taskpkg.StatusCompleted,
		OutputFile:   "/tmp/agent-1.jsonl",
		Summary:      "Write gin demo",
		Result:       "done",
		TotalTokens:  1200,
		ToolUses:     7,
		DurationMs:   4200,
		WorktreePath: "/tmp/wt",
	}})
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	text := notificationText(t, messages[0])
	for _, want := range []string{
		"<task-notification>", "</task-notification>",
		"<task-id>agent-1</task-id>",
		"<tool-use-id>call-9</tool-use-id>",
		"<output-file>/tmp/agent-1.jsonl</output-file>",
		"<status>completed</status>",
		"<summary>Write gin demo</summary>",
		"<result>done</result>",
		"<total_tokens>1200</total_tokens>",
		"<tool_uses>7</tool_uses>",
		"<duration_ms>4200</duration_ms>",
		"<worktreePath>/tmp/wt</worktreePath>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, `"task_id"`) {
		t.Fatalf("notification still uses JSON payload:\n%s", text)
	}
}

// killed 通知必须带 reason,且空 result 整段省略
func TestNotificationXMLOmitsEmptySections(t *testing.T) {
	messages := subagentNotificationMessages([]taskpkg.Notification{{
		TaskID:  "agent-2",
		Status:  taskpkg.StatusKilled,
		Reason:  taskpkg.StopReasonUserStop,
		Summary: `Agent "x" was stopped by user`,
	}})
	text := notificationText(t, messages[0])
	if !strings.Contains(text, "<reason>user_stop</reason>") {
		t.Fatalf("missing reason:\n%s", text)
	}
	if strings.Contains(text, "<result>") {
		t.Fatalf("empty result should be omitted:\n%s", text)
	}
	if strings.Contains(text, "<worktree>") {
		t.Fatalf("empty worktree should be omitted:\n%s", text)
	}
	if strings.Contains(text, "<output-file>") {
		t.Fatalf("empty output file should be omitted:\n%s", text)
	}
}

// summary 与 result 里的标记必须转义,不能破坏 XML 结构
func TestNotificationXMLEscapesMarkup(t *testing.T) {
	messages := subagentNotificationMessages([]taskpkg.Notification{{
		TaskID:  "agent-3",
		Status:  taskpkg.StatusCompleted,
		Summary: "a < b & c",
		Result:  "x < y & z",
	}})
	text := notificationText(t, messages[0])
	if !strings.Contains(text, "&lt;") || !strings.Contains(text, "&amp;") {
		t.Fatalf("markup was not escaped:\n%s", text)
	}
	if strings.Contains(text, "< b") || strings.Contains(text, "& c") {
		t.Fatalf("raw markup leaked into XML:\n%s", text)
	}
}

func notificationText(t *testing.T, message llm.Message) string {
	t.Helper()
	user, ok := message.(llm.UserMessage)
	if !ok {
		t.Fatalf("notification message type = %T, want llm.UserMessage", message)
	}
	var parts []string
	for _, content := range user.Content {
		if text, ok := content.(llm.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
