package subagent

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"

	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// AgentRuntime 必须满足 task.Runtime，store 才能在不知道具体 agent 的情况下驱动它
func TestAgentRuntimeSatisfiesTaskRuntime(t *testing.T) {
	concrete := &AgentRuntime{DoneChan: make(chan struct{})}
	var runtime taskpkg.Runtime = concrete
	if runtime.Done() == nil {
		t.Fatal("Done() = nil, want a channel")
	}
	if runtime.OutputPath() != "" || runtime.Worktree() != "" {
		t.Error("empty runtime should report empty paths")
	}
	runtime.SetToken(7)
	if concrete.LifecycleToken != 7 {
		t.Errorf("token = %d, want 7", concrete.LifecycleToken)
	}
}

// Abort 暴露取消函数，未设置时返回 nil 供 store 判断
func TestAgentRuntimeAbortReturnsCancel(t *testing.T) {
	called := false
	runtime := &AgentRuntime{DoneChan: make(chan struct{}), Cancel: func() { called = true }}
	abort := runtime.Abort()
	if abort == nil {
		t.Fatal("Abort() = nil, want cancel func")
	}
	abort()
	if !called {
		t.Error("Abort() did not invoke the stored cancel func")
	}
	if (&AgentRuntime{}).Abort() != nil {
		t.Error("Abort() without cancel should be nil")
	}
}

// 运行入口与上下文由 runtime 自己持有，store 不感知
func TestAgentRuntimeCarriesRunEntry(t *testing.T) {
	runtime := &AgentRuntime{
		DoneChan:     make(chan struct{}),
		OutputFile:   "/tmp/child.jsonl",
		WorktreePath: "/tmp/wt",
		MessageStart: 3,
		Run: func(context.Context, string) (*llm.AssistantMessage, error) {
			return nil, nil
		},
	}
	if runtime.OutputPath() != "/tmp/child.jsonl" || runtime.Worktree() != "/tmp/wt" {
		t.Errorf("paths = %q/%q", runtime.OutputPath(), runtime.Worktree())
	}
	if runtime.MessageStart != 3 {
		t.Errorf("MessageStart = %d, want 3", runtime.MessageStart)
	}
	if runtime.Run == nil {
		t.Error("Run entry lost")
	}
}
