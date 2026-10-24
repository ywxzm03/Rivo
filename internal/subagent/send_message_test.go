package subagent

import (
	"context"
	"errors"
	"testing"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func sendMessageManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(NewStore(), func(_ context.Context, spec AgentTaskSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: spec.Prompt}}}, nil
		}
		return runtime, nil
	})
}

func TestSendMessageQueuesForRunningTask(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	manager := NewManager(NewStore(), func(context.Context, AgentTaskSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(ctx context.Context, _ string) (*llm.AssistantMessage, error) {
			close(started)
			<-release
			return &llm.AssistantMessage{}, nil
		}
		return runtime, nil
	})
	launched, err := manager.Start(context.Background(), AgentToolInput{Description: "long", Prompt: "work"})
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
	waitForTaskStatus(t, manager.Store(), launched.AgentID, StateCompleted)
}

func TestSendMessageResumesTerminalTask(t *testing.T) {
	manager := sendMessageManager(t)
	launched, err := manager.Start(context.Background(), AgentToolInput{Description: "probe", Prompt: "ALPHA"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, StateCompleted)
	manager.Store().DrainNotifications()

	result, err := manager.SendMessage(context.Background(), launched.AgentID, "BETA")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "resumed" || result.TaskID != launched.AgentID {
		t.Fatalf("send result = %#v", result)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, StateCompleted)
	notifications := manager.Store().DrainNotifications()
	if len(notifications) != 1 || notifications[0].TaskID != launched.AgentID || notifications[0].Result != "BETA" {
		t.Fatalf("continuation notifications = %#v", notifications)
	}
}

func TestSendMessageResumesEvictedTaskFromResolver(t *testing.T) {
	manager := sendMessageManager(t)
	var factorySpec AgentTaskSpec
	base := manager.factory
	manager.SetFactory(func(ctx context.Context, spec AgentTaskSpec) (*AgentRuntime, error) {
		factorySpec = spec
		return base(ctx, spec)
	})
	manager.SetResumeResolver(func(taskID string) (ResumeSource, bool) {
		if taskID != "agent-evicted-1" {
			return ResumeSource{}, false
		}
		return ResumeSource{ID: taskID, AgentType: "explore", Description: "old work", OutputFile: "/tmp/agent-evicted-1.jsonl"}, true
	})

	result, err := manager.SendMessage(context.Background(), "agent-evicted-1", "continue please")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "resumed" || result.TaskID != "agent-evicted-1" {
		t.Fatalf("send result = %#v", result)
	}
	// 驱逐任务必须按 continuation 语义重建,复用原 transcript
	if !factorySpec.Continuation || factorySpec.OutputFile != "/tmp/agent-evicted-1.jsonl" || factorySpec.AgentType != "explore" {
		t.Fatalf("factory spec = %#v", factorySpec)
	}
	waitForTaskStatus(t, manager.Store(), "agent-evicted-1", StateCompleted)
}

func TestSendMessageRejectsUnknownTask(t *testing.T) {
	manager := sendMessageManager(t)
	_, err := manager.SendMessage(context.Background(), "agent-missing", "hello")
	var stopErr *StopTaskError
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
