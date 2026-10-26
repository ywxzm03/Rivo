package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// fakeStream 是返回固定文本的流,按调用次数推进
func notifyStream(texts ...string) llm.StreamFn {
	index := 0
	return func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		text := "ok"
		if index < len(texts) {
			text = texts[index]
		}
		index++
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: text}}}}
		close(events)
		return events, nil
	}
}

// 主 agent 空闲时收到子任务终态通知必须被唤醒,否则通知永远留在队列里
func TestIdleMainAgentWakesForTaskNotification(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a.agent.StreamFn = notifyStream("woke up")
	store := a.SubagentStore()
	if store == nil {
		t.Fatal("subagent store is unavailable")
	}
	// 1.注册一个已运行的任务并把它转成 killed,再投递通知
	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "victim", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, &taskpkg.AgentResult{AgentID: taskID}) {
		t.Fatal("task did not transition to completed")
	}
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "victim"}) {
		t.Fatal("notification was not enqueued")
	}

	// 2.空闲的主 agent 必须自己开始一次 run 来消费通知
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(store.PeekNotifications()) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("notification still pending after 2s: %+v", store.PeekNotifications())
}

// 通知必须真的进入主 agent 的请求上下文,而不只是从队列移除
func TestNotificationReachesMainAgentContext(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	inner := notifyStream("acknowledged")
	a.agent.StreamFn = func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		for _, message := range c.Messages {
			user, ok := message.(llm.UserMessage)
			if !ok {
				continue
			}
			for _, content := range user.Content {
				if text, ok := content.(llm.TextContent); ok {
					seen = append(seen, text.Text)
				}
			}
		}
		return inner(ctx, model, c, opts)
	}
	store := a.SubagentStore()
	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "done", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, &taskpkg.AgentResult{AgentID: taskID}) {
		t.Fatal("task did not transition to completed")
	}
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "done"}) {
		t.Fatal("notification was not enqueued")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(store.PeekNotifications()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 等 run 真正把请求发出去
	time.Sleep(100 * time.Millisecond)
	found := false
	for _, text := range seen {
		if strings.Contains(text, "<task-notification>") && strings.Contains(text, taskID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("notification missing from main agent request context: %q", seen)
	}
}

// 收尾后到达的通知不得再起 run,否则退出过程中会凭空多出一轮模型调用
func TestShutdownStopsNotificationRuns(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := atomic.Int64{}
	inner := notifyStream("late")
	a.agent.StreamFn = func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls.Add(1)
		return inner(ctx, model, c, opts)
	}
	store := a.SubagentStore()
	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "late", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, &taskpkg.AgentResult{AgentID: taskID}) {
		t.Fatal("task did not transition to completed")
	}

	a.ShutdownSubagents()
	before := calls.Load()
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "late"}) {
		t.Fatal("notification was not enqueued")
	}
	time.Sleep(300 * time.Millisecond)
	if after := calls.Load(); after != before {
		t.Fatalf("model was called %d times after shutdown", after-before)
	}
}

// 后台 run 结束后必须调用结束回调,显示层的忙碌态不能依赖 AgentEndEvent 是否送达
func TestBackgroundRunEndHookFires(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a.agent.StreamFn = notifyStream("done")
	ended := make(chan struct{}, 4)
	a.SetBackgroundRunHook(func() { ended <- struct{}{} })
	store := a.SubagentStore()
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
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("background run end hook never fired")
	}
}

// Claude 的 runAsyncAgentLifecycle 在 kill 后先转终态,再把 stopped 通知交给父 agent
func TestClaudeContractStopSubagentReachesParentAgent(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	a.subagents.SetFactory(func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime := &taskpkg.AgentRuntime{
			Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil),
			Done:  make(chan struct{}),
		}
		runtime.Run = func(ctx context.Context, _ string) (*llm.AssistantMessage, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return runtime, nil
	})

	taskIDCh := make(chan string, 1)
	seen := make(chan string, 1)
	a.agent.StreamFn = func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		for _, message := range c.Messages {
			user, ok := message.(llm.UserMessage)
			if !ok {
				continue
			}
			for _, content := range user.Content {
				text, ok := content.(llm.TextContent)
				if !ok || !strings.Contains(text.Text, "<task-notification>") {
					continue
				}
				taskID := <-taskIDCh
				task, ok := a.SubagentStore().Task(taskID)
				if !ok || task.Base().Status != taskpkg.StatusKilled {
					t.Errorf("parent saw notification before killed state: %#v", task)
				}
				seen <- text.Text
			}
		}
		return notifyStream("parent noticed")(ctx, model, c, opts)
	}

	launched, err := a.subagents.Start(context.Background(), taskpkg.AgentInput{
		Description: "background inspection",
		Prompt:      "inspect slowly",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	taskIDCh <- launched.AgentID
	if err := a.StopSubagent(launched.AgentID); err != nil {
		t.Fatal(err)
	}

	select {
	case text := <-seen:
		if !strings.Contains(text, "<status>killed</status>") || !strings.Contains(text, "<reason>user_stop</reason>") || !strings.Contains(text, "was stopped by user") {
			t.Fatalf("parent notification = %q, want killed user_stop payload", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent agent did not receive the killed notification")
	}
}
