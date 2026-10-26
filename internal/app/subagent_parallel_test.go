package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// 子任务在后台跑时主 agent 必须能继续发起新的 run
func TestMainAgentRunsWhileSubagentIsRunning(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a.agent.StreamFn = notifyStream("first reply", "second reply")

	store := a.SubagentStore()
	taskID, _, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "long job", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	// 任务保持在 running,模拟仍在后台执行的子 agent
	if state, ok := store.Task(taskID); !ok || state.Base().Status != taskpkg.StatusRunning {
		t.Fatalf("task state = %+v, want running", state)
	}

	if _, err := a.Run(context.Background(), "first"); err != nil {
		t.Fatalf("first run failed while subagent running: %v", err)
	}
	if _, err := a.Run(context.Background(), "second"); err != nil {
		t.Fatalf("second run failed while subagent running: %v", err)
	}
}

// 用户 kill 子任务的同时主 agent 正在跑,两者不能互相阻塞或丢通知
func TestKillDuringMainRunDeliversNotification(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	inner := notifyStream("done")
	a.agent.StreamFn = func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		// 第一轮卡住,让 kill 发生在主 agent 运行期间
		once.Do(func() { <-release })
		return inner(ctx, model, c, opts)
	}
	store := a.SubagentStore()
	taskID, _, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "victim", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { _, err := a.Run(context.Background(), "start"); done <- err }()
	time.Sleep(50 * time.Millisecond)

	// 主 agent 运行中 kill 子任务
	if err := a.StopSubagent(taskID); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("main run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("main run did not finish after kill")
	}

	// kill 的通知必须在收尾或随后的后台 run 里被消费掉
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(store.PeekNotifications()) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("kill notification never delivered: %+v", store.PeekNotifications())
}
