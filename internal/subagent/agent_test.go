package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestManagerStartRunsAndPublishesTerminalNotification(t *testing.T) {
	manager := NewManager(NewStore(), func(context.Context, AgentTaskSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "finished"}}, Usage: llm.Usage{TotalTokens: 3}}, nil
		}
		return runtime, nil
	})
	launched, err := manager.Start(context.Background(), AgentToolInput{Description: "inspect", Prompt: "look around"})
	if err != nil {
		t.Fatal(err)
	}
	if launched.Status != "async_launched" || launched.State != StateRunning {
		t.Fatalf("launch result = %#v", launched)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, StateCompleted)
	notifications := manager.Store().DrainNotifications()
	if len(notifications) != 1 || notifications[0].Status != StateCompleted || notifications[0].Result != "finished" {
		t.Fatalf("notifications = %#v", notifications)
	}
}

func TestManagerContinueReusesTaskID(t *testing.T) {
	manager := NewManager(NewStore(), func(context.Context, AgentTaskSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}}, nil
		}
		return runtime, nil
	})
	first, err := manager.Start(context.Background(), AgentToolInput{Description: "inspect", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), first.AgentID, StateCompleted)
	manager.Store().DrainNotifications()
	second, err := manager.Continue(context.Background(), first.AgentID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if second.AgentID != first.AgentID || second.Status != "async_launched" {
		t.Fatalf("continuation result = %#v", second)
	}
	waitForTaskStatus(t, manager.Store(), first.AgentID, StateCompleted)
	if got := manager.Store().DrainNotifications(); len(got) != 1 || got[0].TaskID != first.AgentID {
		t.Fatalf("continuation notifications = %#v", got)
	}
}

func TestManagerStartDetachesFromToolBatchContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	manager := NewManager(NewStore(), func(context.Context, AgentTaskSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(ctx context.Context, _ string) (*llm.AssistantMessage, error) {
			close(started)
			select {
			case <-release:
				return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "finished"}}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return runtime, nil
	})
	requestCtx, cancel := context.WithCancel(context.Background())
	launched, err := manager.Start(requestCtx, AgentToolInput{Description: "inspect", Prompt: "look around"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	time.Sleep(20 * time.Millisecond)
	if task, ok := manager.Store().Task(launched.AgentID); !ok || task.Base().Status != StateRunning {
		t.Fatalf("background task stopped with the tool batch context: %#v", task)
	}
	close(release)
	waitForTaskStatus(t, manager.Store(), launched.AgentID, StateCompleted)
}

func TestManagerNormalizesClaudeGeneralPurposeAliasToWork(t *testing.T) {
	manager := NewManager(NewStore(), func(context.Context, AgentTaskSpec) (*AgentRuntime, error) {
		runtime := &AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}, nil
		}
		return runtime, nil
	})
	alias := "general-purpose"
	launched, err := manager.Start(context.Background(), AgentToolInput{Description: "work", Prompt: "do it", SubagentType: &alias})
	if err != nil {
		t.Fatal(err)
	}
	if launched.SubagentType != "work" {
		t.Fatalf("normalized type = %q, want work", launched.SubagentType)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, StateCompleted)
}

func waitForTaskStatus(t *testing.T, store *Store, id string, want TaskStatus) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		task, ok := store.Task(id)
		if ok && task.Base().Status == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("task %s did not reach %s: %#v", id, want, task)
		case <-time.After(time.Millisecond):
		}
	}
}
