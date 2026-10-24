package agenttool

import (
	"context"
	"errors"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// fakeLauncher 记录 Start 收到的输入,用于校验工具契约的参数解析
type fakeLauncher struct {
	input taskpkg.AgentInput
	err   error
}

func (f *fakeLauncher) Start(_ context.Context, input taskpkg.AgentInput) (taskpkg.AgentLaunched, error) {
	f.input = input
	if f.err != nil {
		return taskpkg.AgentLaunched{}, f.err
	}
	return taskpkg.AgentLaunched{AgentID: "agent-1", Status: "async_launched"}, nil
}

func TestToolSatisfiesLLMToolContract(t *testing.T) {
	var _ llm.Tool = New(&fakeLauncher{})
}

func TestToolMetadata(t *testing.T) {
	tool := New(&fakeLauncher{})
	if got := tool.Name(); got != "agent" {
		t.Fatalf("Name() = %q, want agent", got)
	}
	if tool.Description() == "" {
		t.Fatal("Description() is empty")
	}
	if tool.PromptSnippet() == "" {
		t.Fatal("PromptSnippet() is empty")
	}
	if got := tool.ExecutionMode(); got != llm.ToolExecutionParallel {
		t.Fatalf("ExecutionMode() = %q, want parallel", got)
	}
	// 1.参数 schema 必须声明必填项,否则模型会漏传 prompt
	schema := tool.Parameters()
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 {
		t.Fatalf("required = %#v, want description and prompt", schema["required"])
	}
}

func TestExecuteParsesFullInput(t *testing.T) {
	launcher := &fakeLauncher{}
	tool := New(launcher)
	result, err := tool.Execute(context.Background(), map[string]any{
		"description":   "inspect tree",
		"prompt":        "list files",
		"name":          "scout",
		"subagent_type": "explore",
		"worktree":      true,
		"thinking":      "high",
		"model":         "anthropic/claude-sonnet-4",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := launcher.input
	if input.Description != "inspect tree" || input.Prompt != "list files" || input.Name != "scout" {
		t.Fatalf("basic fields = %#v", input)
	}
	if input.SubagentType == nil || *input.SubagentType != "explore" {
		t.Fatalf("subagent_type = %#v", input.SubagentType)
	}
	if input.Worktree == nil || !*input.Worktree {
		t.Fatalf("worktree = %#v", input.Worktree)
	}
	if input.Thinking == nil || *input.Thinking != llm.ThinkingLevelHigh {
		t.Fatalf("thinking = %#v", input.Thinking)
	}
	// 2.provider/id 形式要拆成两段
	if input.Model == nil || input.Model.Provider != "anthropic" || input.Model.ID != "claude-sonnet-4" {
		t.Fatalf("model = %#v", input.Model)
	}
	// 3.结果正文回带 agent ID,details 携带完整启动信息
	if text := result.Content[0].(llm.TextContent).Text; text != "Started subagent agent-1" {
		t.Fatalf("result text = %q", text)
	}
	if details, ok := result.Details.(taskpkg.AgentLaunched); !ok || details.AgentID != "agent-1" {
		t.Fatalf("result details = %#v", result.Details)
	}
}

func TestExecuteOmitsOptionalFields(t *testing.T) {
	launcher := &fakeLauncher{}
	tool := New(launcher)
	if _, err := tool.Execute(context.Background(), map[string]any{"description": "d", "prompt": "p"}); err != nil {
		t.Fatal(err)
	}
	input := launcher.input
	if input.SubagentType != nil || input.Worktree != nil || input.Thinking != nil || input.Model != nil {
		t.Fatalf("optional fields should stay nil: %#v", input)
	}
}

func TestExecuteSplitsBareModelID(t *testing.T) {
	launcher := &fakeLauncher{}
	tool := New(launcher)
	if _, err := tool.Execute(context.Background(), map[string]any{"description": "d", "prompt": "p", "model": "claude-sonnet-4"}); err != nil {
		t.Fatal(err)
	}
	// 不含斜杠时整串都当 ID,provider 留空
	if input := launcher.input; input.Model == nil || input.Model.Provider != "" || input.Model.ID != "claude-sonnet-4" {
		t.Fatalf("model = %#v", launcher.input.Model)
	}
}

func TestExecutePropagatesLauncherError(t *testing.T) {
	wantErr := errors.New("launch failed")
	tool := New(&fakeLauncher{err: wantErr})
	if _, err := tool.Execute(context.Background(), map[string]any{"description": "d", "prompt": "p"}); err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
