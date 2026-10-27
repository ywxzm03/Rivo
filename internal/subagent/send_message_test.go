package subagent

import (
	"context"
	"errors"
	"testing"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

func sendMessageManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(taskpkg.NewStore(), func(_ context.Context, spec taskpkg.AgentSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.New(agent.Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil}), DoneChan: make(chan struct{})}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: spec.Prompt}}}, nil
		}
		return runtime, nil
	})
}

func TestSendMessageQueuesForRunningTask(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.New(agent.Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil}), DoneChan: make(chan struct{})}
		runtime.Run = func(ctx context.Context, _ string) (*llm.AssistantMessage, error) {
			close(started)
			<-release
			return &llm.AssistantMessage{}, nil
		}
		return runtime, nil
	})
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "long", Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	result, err := manager.SendMessage(context.Background(), launched.AgentID, "extra work")
	if err != nil {
		t.Fatal(err)
	}
	// 运行中的任务只入队,不启动第二个 query
	if result.Status != "queued" || result.TaskID != launched.AgentID {
		t.Fatalf("send result = %#v", result)
	}
	if pending := manager.Store().PendingMessages(launched.AgentID); len(pending) != 1 {
		t.Fatalf("pending = %#v, want one queued message", pending)
	}
	close(release)
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
}

func TestSendMessageResumesTerminalTask(t *testing.T) {
	manager := sendMessageManager(t)
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "probe", Prompt: "ALPHA"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
	manager.Store().DrainNotifications()

	result, err := manager.SendMessage(context.Background(), launched.AgentID, "BETA")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "resumed" || result.TaskID != launched.AgentID {
		t.Fatalf("send result = %#v", result)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
	notifications := manager.Store().DrainNotifications()
	if len(notifications) != 1 || notifications[0].TaskID != launched.AgentID || notifications[0].Result != "BETA" {
		t.Fatalf("continuation notifications = %#v", notifications)
	}
}

func TestSendMessageResumesEvictedTaskFromResolver(t *testing.T) {
	manager := sendMessageManager(t)
	var factorySpec taskpkg.AgentSpec
	base := manager.factory
	manager.SetFactory(func(ctx context.Context, spec taskpkg.AgentSpec) (*AgentRuntime, error) {
		factorySpec = spec
		return base(ctx, spec)
	})
	manager.SetResumeResolver(func(taskID string) (taskpkg.ResumeSource, bool) {
		if taskID != "agent-evicted-1" {
			return taskpkg.ResumeSource{}, false
		}
		return taskpkg.ResumeSource{ID: taskID, AgentType: "explore", Description: "old work", OutputFile: "/tmp/agent-evicted-1.jsonl", WorktreePath: "/tmp/wt"}, true
	})

	result, err := manager.SendMessage(context.Background(), "agent-evicted-1", "continue please")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "resumed" || result.TaskID != "agent-evicted-1" {
		t.Fatalf("send result = %#v", result)
	}
	// 驱逐任务必须按 continuation 语义重建,复用原 transcript
	if !factorySpec.Continuation || factorySpec.WorktreePath != "/tmp/wt" || factorySpec.AgentType != "explore" {
		t.Fatalf("factory spec = %#v", factorySpec)
	}
	// 恢复来的 worktree 保留用户工作,不能安装清理钩子
	if factorySpec.Worktree {
		t.Fatalf("factory spec = %#v, want Worktree disabled for resumed worktree", factorySpec)
	}
	waitForTaskStatus(t, manager.Store(), "agent-evicted-1", taskpkg.StatusCompleted)
}

func TestSendMessageResumeKeepsOwnedWorktree(t *testing.T) {
	manager := sendMessageManager(t)
	var factorySpec taskpkg.AgentSpec
	base := manager.factory
	manager.SetFactory(func(ctx context.Context, spec taskpkg.AgentSpec) (*AgentRuntime, error) {
		factorySpec = spec
		return base(ctx, spec)
	})
	// 元数据标明该 worktree 由子 agent 创建,恢复时依然不能回收
	manager.SetResumeResolver(func(string) (taskpkg.ResumeSource, bool) {
		return taskpkg.ResumeSource{
			ID: "agent-evicted-2", AgentType: "work", Description: "old work",
			OutputFile: "/tmp/agent-evicted-2.jsonl", WorktreePath: "/tmp/wt", WorktreeOwned: true,
		}, true
	})
	if _, err := manager.SendMessage(context.Background(), "agent-evicted-2", "continue please"); err != nil {
		t.Fatal(err)
	}
	if factorySpec.WorktreePath != "/tmp/wt" || factorySpec.Worktree {
		t.Fatalf("factory spec = %#v, want path kept and cleanup disabled", factorySpec)
	}
	waitForTaskStatus(t, manager.Store(), "agent-evicted-2", taskpkg.StatusCompleted)
}

func TestSendMessageRejectsUnknownTask(t *testing.T) {
	manager := sendMessageManager(t)
	_, err := manager.SendMessage(context.Background(), "agent-missing", "hello")
	var stopErr *taskpkg.StopTaskError
	if !errors.As(err, &stopErr) || stopErr.Code != "not_found" {
		t.Fatalf("error = %v, want not_found", err)
	}
}

func TestSendMessageRejectsEmptyInput(t *testing.T) {
	manager := sendMessageManager(t)
	if _, err := manager.SendMessage(context.Background(), "", "hello"); err == nil {
		t.Fatal("empty task ID was accepted")
	}
	if _, err := manager.SendMessage(context.Background(), "agent-1", "  "); err == nil {
		t.Fatal("empty message was accepted")
	}
}

func TestLatestAssistantWithoutMessages(t *testing.T) {
	runtime := agent.New(agent.Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil})
	if got := latestAssistant(runtime); got != nil {
		t.Fatalf("latestAssistant(empty) = %#v, want nil", got)
	}
}
