package subagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// fakeRuntime 构造一个只跑给定 run 函数的 runtime
func fakeRuntime(run func(context.Context, string) (*llm.AssistantMessage, error)) func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
	return func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime := &taskpkg.AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = run
		return runtime, nil
	}
}

// Start 必须拒绝缺少描述或 prompt 的请求,否则会注册出无法展示的空任务
func TestStartRejectsEmptyInput(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), fakeRuntime(func(context.Context, string) (*llm.AssistantMessage, error) {
		return &llm.AssistantMessage{}, nil
	}))
	if _, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "  ", Prompt: "x"}); err == nil {
		t.Fatal("empty description accepted")
	}
	if _, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "x", Prompt: "  "}); err == nil {
		t.Fatal("empty prompt accepted")
	}
}

// fork 子 agent 内部再次派发必须被拒绝
func TestStartRejectsForkChildRecursion(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), fakeRuntime(func(context.Context, string) (*llm.AssistantMessage, error) {
		return &llm.AssistantMessage{}, nil
	}))
	if _, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "x", Prompt: "y", ForkChild: true}); err == nil {
		t.Fatal("fork child recursion accepted")
	}
}

// 没有 factory 时 Start 必须直接报错,不能注册出永远 running 的任务
func TestStartRejectsMissingFactory(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), nil)
	if _, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "x", Prompt: "y"}); err == nil {
		t.Fatal("missing factory accepted")
	}
}

// general/general-purpose 必须归一化成 work,任务面板只暴露 work
func TestStartNormalizesGeneralPurposeAlias(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), fakeRuntime(func(context.Context, string) (*llm.AssistantMessage, error) {
		return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}}, nil
	}))
	alias := "general-purpose"
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "x", Prompt: "y", SubagentType: &alias})
	if err != nil {
		t.Fatal(err)
	}
	if launched.SubagentType != "work" {
		t.Fatalf("subagent type = %q, want work", launched.SubagentType)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
}

// factory 失败时任务不能残留在 store 里
func TestStartDiscardsTaskWhenFactoryFails(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		return nil, errors.New("factory exploded")
	})
	if _, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "x", Prompt: "y"}); err == nil {
		t.Fatal("factory failure was swallowed")
	}
	if tasks := manager.Store().Snapshot(); len(tasks) != 0 {
		t.Fatalf("tasks = %d, want 0 after launch failure", len(tasks))
	}
}

// Continue 必须拒绝空 task ID 或空 prompt
func TestContinueRejectsEmptyInput(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), fakeRuntime(func(context.Context, string) (*llm.AssistantMessage, error) {
		return &llm.AssistantMessage{}, nil
	}))
	if _, err := manager.Continue(context.Background(), "", "x"); err == nil {
		t.Fatal("empty task ID accepted")
	}
	if _, err := manager.Continue(context.Background(), "task-1", "  "); err == nil {
		t.Fatal("empty prompt accepted")
	}
}

// Continue 对未知任务必须返回 not_found,不能新建任务
func TestContinueRejectsUnknownTask(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), fakeRuntime(func(context.Context, string) (*llm.AssistantMessage, error) {
		return &llm.AssistantMessage{}, nil
	}))
	_, err := manager.Continue(context.Background(), "missing", "x")
	var stopErr *taskpkg.StopTaskError
	if !errors.As(err, &stopErr) || stopErr.Code != "not_found" {
		t.Fatalf("error = %v, want not_found StopTaskError", err)
	}
}

// Continue 对仍在运行的任务必须拒绝,引导调用方改用排队
func TestContinueRejectsRunningTask(t *testing.T) {
	block := make(chan struct{})
	manager := NewManager(taskpkg.NewStore(), fakeRuntime(func(context.Context, string) (*llm.AssistantMessage, error) {
		<-block
		return &llm.AssistantMessage{}, nil
	}))
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "x", Prompt: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Continue(context.Background(), launched.AgentID, "again"); err == nil {
		t.Fatal("continuation of a running task was accepted")
	}
	close(block)
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
}

// run 失败时任务转 failed 并投递 failed 通知,不能留在 running
func TestRunFailureTransitionsToFailedWithNotification(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), fakeRuntime(func(context.Context, string) (*llm.AssistantMessage, error) {
		return nil, errors.New("boom")
	}))
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "fails", Prompt: "y"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusFailed)
	notifications := manager.Store().DrainNotifications()
	if len(notifications) != 1 || notifications[0].Status != taskpkg.StatusFailed {
		t.Fatalf("notifications = %#v, want one failed notification", notifications)
	}
	if notifications[0].Summary == "" {
		t.Fatal("failed notification carries no error summary")
	}
}

// panic 必须被兜成 failed,并照常关闭 Done、移除 runtime
func TestRunPanicTransitionsToFailed(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), fakeRuntime(func(context.Context, string) (*llm.AssistantMessage, error) {
		panic("kaboom")
	}))
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "panics", Prompt: "y"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusFailed)
	notifications := manager.Store().DrainNotifications()
	if len(notifications) != 1 || notifications[0].Status != taskpkg.StatusFailed {
		t.Fatalf("notifications = %#v, want one failed notification", notifications)
	}
}

// 运行中被 kill 的任务必须投递 killed 通知并带上已产出的部分结果
func TestKilledTaskDeliversPartialResult(t *testing.T) {
	started := make(chan struct{})
	manager := NewManager(taskpkg.NewStore(), func(_ context.Context, _ taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime := &taskpkg.AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(ctx context.Context, _ string) (*llm.AssistantMessage, error) {
			// 先产出一条 assistant,再等待取消
			runtime.Agent.AgentContext.Messages = append(runtime.Agent.AgentContext.Messages, llm.AssistantMessage{
				Content: []llm.Content{llm.TextContent{Text: "partial finding"}},
			})
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return runtime, nil
	})
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "slow", Prompt: "y"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := taskpkg.KillAgentWithReason(launched.AgentID, manager.Store(), taskpkg.StopReasonUserStop); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusKilled)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		notifications := manager.Store().PeekNotifications()
		if len(notifications) == 1 {
			if notifications[0].Status != taskpkg.StatusKilled {
				t.Fatalf("status = %q, want killed", notifications[0].Status)
			}
			if notifications[0].Result != "partial finding" {
				t.Fatalf("result = %q, want partial finding", notifications[0].Result)
			}
			if notifications[0].Reason != taskpkg.StopReasonUserStop {
				t.Fatalf("reason = %q, want user_stop", notifications[0].Reason)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no killed notification delivered")
}

// latestAssistant 在没有 assistant 消息时返回 nil,不能 panic
func TestLatestAssistantWithoutMessages(t *testing.T) {
	if got := latestAssistant(nil); got != nil {
		t.Fatalf("latestAssistant(nil) = %#v, want nil", got)
	}
	runtime := agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil)
	if got := latestAssistant(runtime); got != nil {
		t.Fatalf("latestAssistant(empty) = %#v, want nil", got)
	}
}
