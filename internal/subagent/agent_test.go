package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

func TestManagerStartRunsAndPublishesTerminalNotification(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime := &taskpkg.AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "finished"}}, Usage: llm.Usage{TotalTokens: 3}}, nil
		}
		return runtime, nil
	})
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "inspect", Prompt: "look around"})
	if err != nil {
		t.Fatal(err)
	}
	if launched.Status != "async_launched" || launched.State != taskpkg.StatusRunning {
		t.Fatalf("launch result = %#v", launched)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
	notifications := manager.Store().DrainNotifications()
	if len(notifications) != 1 || notifications[0].Status != taskpkg.StatusCompleted || notifications[0].Result != "finished" {
		t.Fatalf("notifications = %#v", notifications)
	}
}

func TestManagerContinueReusesTaskID(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime := &taskpkg.AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}}, nil
		}
		return runtime, nil
	})
	first, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "inspect", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), first.AgentID, taskpkg.StatusCompleted)
	manager.Store().DrainNotifications()
	second, err := manager.Continue(context.Background(), first.AgentID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if second.AgentID != first.AgentID || second.Status != "async_launched" {
		t.Fatalf("continuation result = %#v", second)
	}
	waitForTaskStatus(t, manager.Store(), first.AgentID, taskpkg.StatusCompleted)
	if got := manager.Store().DrainNotifications(); len(got) != 1 || got[0].TaskID != first.AgentID {
		t.Fatalf("continuation notifications = %#v", got)
	}
}

func TestManagerStartDetachesFromToolBatchContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime := &taskpkg.AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
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
	launched, err := manager.Start(requestCtx, taskpkg.AgentInput{Description: "inspect", Prompt: "look around"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	time.Sleep(20 * time.Millisecond)
	if task, ok := manager.Store().Task(launched.AgentID); !ok || task.Base().Status != taskpkg.StatusRunning {
		t.Fatalf("background task stopped with the tool batch context: %#v", task)
	}
	close(release)
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
}

func TestManagerNormalizesClaudeGeneralPurposeAliasToWork(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime := &taskpkg.AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}, nil
		}
		return runtime, nil
	})
	alias := "general-purpose"
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "work", Prompt: "do it", SubagentType: &alias})
	if err != nil {
		t.Fatal(err)
	}
	if launched.SubagentType != "work" {
		t.Fatalf("normalized type = %q, want work", launched.SubagentType)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusCompleted)
}

func TestManagerRecoversPanickingRuntime(t *testing.T) {
	var runtime *taskpkg.AgentRuntime
	cleaned := make(chan struct{})
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime = &taskpkg.AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.WorktreeCleanup = func() bool {
			close(cleaned)
			return true
		}
		runtime.Run = func(context.Context, string) (*llm.AssistantMessage, error) {
			panic("runtime blew up")
		}
		return runtime, nil
	})
	launched, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "inspect", Prompt: "look around"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, manager.Store(), launched.AgentID, taskpkg.StatusFailed)
	task, ok := manager.Store().Task(launched.AgentID)
	if !ok || !strings.Contains(task.(*taskpkg.AgentState).Error, "panic: runtime blew up") {
		t.Fatalf("task error = %#v", task)
	}
	select {
	case <-runtime.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("Done was not closed after a panicking run")
	}
	select {
	case <-cleaned:
	case <-time.After(2 * time.Second):
		t.Fatal("worktree cleanup was skipped after a panicking run")
	}
	notifications := manager.Store().DrainNotifications()
	if len(notifications) != 1 || notifications[0].Status != taskpkg.StatusFailed || !strings.Contains(notifications[0].Summary, "panic: runtime blew up") {
		t.Fatalf("notifications = %#v", notifications)
	}
}

// Claude 的 async_launched 任务彼此独立:两个后台 agent 必须同时启动,不能串行占用父 agent
func TestClaudeContractAsyncAgentsRunConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	manager := NewManager(taskpkg.NewStore(), func(_ context.Context, spec taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		runtime := &taskpkg.AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
		runtime.Run = func(_ context.Context, _ string) (*llm.AssistantMessage, error) {
			started <- spec.ID
			<-release
			return &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: spec.ID + " done"}}}, nil
		}
		return runtime, nil
	})

	first, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "first", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "second", Prompt: "second"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatalf("only %d background agents started: %v", len(seen), seen)
		}
	}
	if !seen[first.AgentID] || !seen[second.AgentID] {
		t.Fatalf("started agents = %v, want %q and %q", seen, first.AgentID, second.AgentID)
	}
	close(release)
	waitForTaskStatus(t, manager.Store(), first.AgentID, taskpkg.StatusCompleted)
	waitForTaskStatus(t, manager.Store(), second.AgentID, taskpkg.StatusCompleted)
}

func waitForTaskStatus(t *testing.T, store *taskpkg.Store, id string, want taskpkg.Status) {
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

// fork 子 agent 保留 Agent 工具只为工具定义一致,递归派发必须在调用边界拒绝
func TestStartRejectsForkChild(t *testing.T) {
	manager := NewManager(taskpkg.NewStore(), func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
		t.Fatal("factory must not run for a rejected fork child")
		return nil, nil
	})
	_, err := manager.Start(context.Background(), taskpkg.AgentInput{Description: "d", Prompt: "p", ForkChild: true})
	if err == nil || !strings.Contains(err.Error(), "fork is not available") {
		t.Fatalf("fork child was not rejected: %v", err)
	}
}

// 子 agent 收尾时必须回收它启动的后台 bash 任务
func TestRunAgentKillsOrphanedBashTasks(t *testing.T) {
	store := taskpkg.NewStore()
	orphanID, orphanToken, err := store.Register(taskpkg.RegisterSpec{
		Type: taskpkg.TypeBash, Description: "orphan", BashState: &taskpkg.BashState{ParentAgentID: "agent-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, nil)
	runtime := &taskpkg.AgentRuntime{
		Agent:  agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil),
		Done:   make(chan struct{}),
		Run:    func(context.Context, string) (*llm.AssistantMessage, error) { return nil, errors.New("boom") },
		Cancel: func() {},
	}
	manager.runAgent(context.Background(), "agent-1", 1, runtime, taskpkg.AgentInput{Description: "d", Prompt: "p"})
	task, ok := store.Task(orphanID)
	if !ok {
		t.Fatal("orphan bash task disappeared")
	}
	if task.Base().Status != taskpkg.StatusKilled {
		t.Fatalf("orphan status = %v, want killed", task.Base().Status)
	}
	_ = orphanToken
}

// 用量必须跨轮累加:只取最后一条 assistant 会严重低估多轮子任务
func TestAgentResultSumsAcrossTurns(t *testing.T) {
	messages := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "go"}}},
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{ID: "c1", Name: "read"},
			llm.ToolCall{ID: "c2", Name: "ls"},
		}, Usage: llm.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}},
		llm.ToolResultMessage{ToolCallID: "c1"},
		llm.ToolResultMessage{ToolCallID: "c2"},
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{ID: "c3", Name: "grep"},
			llm.TextContent{Text: "first"},
		}, Usage: llm.Usage{InputTokens: 300, OutputTokens: 30, TotalTokens: 330}},
		llm.AssistantMessage{Content: []llm.Content{
			llm.TextContent{Text: "second"},
		}, Usage: llm.Usage{InputTokens: 500, OutputTokens: 40, TotalTokens: 540}},
	}
	reply := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}, Usage: llm.Usage{InputTokens: 500, OutputTokens: 40}}
	result := agentResult("agent-1", messages, 0, reply, time.Second)
	if result.TotalToolUseCount != 3 {
		t.Fatalf("tool uses = %d, want 3", result.TotalToolUseCount)
	}
	// 只累加各轮 output 20+30+40;reply 已在 messages 中,不重复计
	if result.TotalTokens != 90 {
		t.Fatalf("total tokens = %d, want 90", result.TotalTokens)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "done" {
		t.Fatalf("content = %+v", result.Content)
	}
}

// 通知正文要拼接全部文本块,不能只取第一块
func TestJoinTextConcatenatesAllBlocks(t *testing.T) {
	blocks := []taskpkg.TextBlock{{Text: "first"}, {Text: "  "}, {Text: "second"}}
	if got := joinText(blocks); got != "first\nsecond" {
		t.Fatalf("joinText = %q, want %q", got, "first\nsecond")
	}
}

// fork 继承的父消息不能计入子任务用量
func TestAgentResultSkipsInheritedMessages(t *testing.T) {
	parent := []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "p1", Name: "read"}}, Usage: llm.Usage{OutputTokens: 500}},
	}
	child := append(append([]llm.Message{}, parent...),
		llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "c1", Name: "grep"}}, Usage: llm.Usage{OutputTokens: 7}},
	)
	reply := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}, Usage: llm.Usage{OutputTokens: 3}}
	result := agentResult("agent-1", child, len(parent), reply, time.Second)
	if result.TotalToolUseCount != 1 {
		t.Fatalf("tool uses = %d, want 1 child call", result.TotalToolUseCount)
	}
	if result.TotalTokens != 7 {
		t.Fatalf("total tokens = %d, want 7 child output tokens", result.TotalTokens)
	}
}
