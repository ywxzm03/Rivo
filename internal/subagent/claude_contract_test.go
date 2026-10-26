package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// Claude 的 LocalAgentTask.killAsyncAgent 契约:停止必须让父 agent 看见一次带明确原因的 killed 通知
func TestClaudeContractKillingAsyncAgentNotifiesParentAsStopped(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{
			Agent:    agent.New(agent.Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil}),
			DoneChan: make(chan struct{}),
		}
		runtime.Run = func(ctx context.Context, _ string) (*llm.AssistantMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return runtime, nil
	})

	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{
		Description: "inspect repository",
		Prompt:      "inspect the repository",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusRunning)

	if err := taskpkg.KillAgent(launched.AgentID, manager.Store()); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusKilled)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		notifications := manager.Store().PeekNotifications()
		if len(notifications) == 1 {
			notification := notifications[0]
			if notification.Status != taskpkg.StatusKilled {
				t.Fatalf("notification status = %q, want killed", notification.Status)
			}
			if notification.Reason != taskpkg.StopReasonUserStop {
				t.Fatalf("notification reason = %q, want user_stop", notification.Reason)
			}
			if notification.Summary != `Agent "inspect repository" was stopped by user` {
				t.Fatalf("notification summary = %q, want explicit user stop summary", notification.Summary)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("killed notification was not delivered: %#v", manager.Store().PeekNotifications())
}

// killAsyncAgent 可能早于 runtime 绑定发生,启动窗口也必须保留 killed 通知
func TestClaudeContractKillDuringLaunchStillNotifiesParent(t *testing.T) {
	factoryEntered := make(chan string, 1)
	releaseFactory := make(chan struct{})
	startResult := make(chan error, 1)
	manager := NewManager(taskpkg.NewStore(), func(_ context.Context, spec taskpkg.AgentSpec) (*AgentRuntime, error) {
		factoryEntered <- spec.ID
		<-releaseFactory
		return &AgentRuntime{
			Agent:    agent.New(agent.Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil}),
			DoneChan: make(chan struct{}),
		}, nil
	})

	go func() {
		_, err := manager.Start(context.Background(), taskpkg.AgentInput{
			Description: "launching inspection",
			Prompt:      "inspect after launch",
		})
		startResult <- err
	}()

	var taskID string
	select {
	case taskID = <-factoryEntered:
	case <-time.After(time.Second):
		t.Fatal("runtime factory was not entered")
	}
	if err := taskpkg.KillAgent(taskID, manager.Store()); err != nil {
		t.Fatal(err)
	}
	close(releaseFactory)
	<-startResult

	waitForTaskStatus(t, manager.Store(), taskID, taskpkg.StatusKilled)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		notifications := manager.Store().PeekNotifications()
		if len(notifications) == 1 {
			if notifications[0].Status != taskpkg.StatusKilled || notifications[0].Reason != taskpkg.StopReasonUserStop || notifications[0].Summary != `Agent "launching inspection" was stopped by user` {
				t.Fatalf("launch-window notification = %#v", notifications[0])
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("launch-window kill notification missing: %#v", manager.Store().PeekNotifications())
}

// runtime 返回 context.Canceled 时仍是 killed 终态,但通知原因必须与用户主动 stop 区分
func TestClaudeContractCancellationNotifiesAbortedReason(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{
			Agent:    agent.New(agent.Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil}),
			DoneChan: make(chan struct{}),
		}
		runtime.Run = func(_ context.Context, _ string) (*llm.AssistantMessage, error) {
			return nil, context.Canceled
		}
		return runtime, nil
	})
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "cancelled work", Prompt: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusKilled)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		notifications := manager.Store().PeekNotifications()
		if len(notifications) == 1 {
			notification := notifications[0]
			if notification.Reason != taskpkg.StopReasonAborted || notification.Summary != `Agent "cancelled work" was aborted` {
				t.Fatalf("cancellation notification = %#v", notification)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("aborted notification was not delivered: %#v", manager.Store().PeekNotifications())
}

// 子任务事件流必须同步更新任务 progress,否则后台行永远显示问号和空活动
func TestClaudeContractWorkerEventsUpdateProgressProjection(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), func(_ context.Context, _ taskpkg.AgentSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.New(agent.Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil}), DoneChan: make(chan struct{})}
		runtime.Run = func(_ context.Context, _ string) (*llm.AssistantMessage, error) {
			_ = runtime.Agent.Emit(llm.ToolExecutionStartEvent{ToolCallID: "call-1", ToolName: "read", Arguments: map[string]any{"path": "a.go"}})
			_ = runtime.Agent.Emit(llm.MessageEndEvent{Message: llm.AssistantMessage{Usage: llm.Usage{OutputTokens: 11}}})
			_ = runtime.Agent.Emit(llm.TurnEndEvent{})
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}, nil
		}
		return runtime, nil
	})
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "eventful work", Prompt: "read a.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
	state, ok := manager.Store().Task(launched.AgentID)
	if !ok {
		t.Fatal("task disappeared")
	}
	progress := state.(*taskpkg.AgentState).Progress
	if progress == nil || progress.ToolUseCount != 1 || progress.TokenCount != 11 || progress.LastActivity == nil || progress.LastActivity.ToolName != "read" {
		t.Fatalf("worker progress = %#v", progress)
	}
}
