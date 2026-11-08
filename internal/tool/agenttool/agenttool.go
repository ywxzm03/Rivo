// Package agenttool 实现 agent 工具契约:把一次任务派发给异步 subagent
package agenttool

import (
	"context"
	"fmt"

	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// Launcher 是工具需要的执行面,由 subagent.Manager 满足。
// 用窄接口而不是直接依赖 Manager,避免 tool 子包与 subagent 包相互引用。
type Launcher interface {
	Start(ctx context.Context, input taskpkg.AgentInput) (taskpkg.AgentLaunched, error)
}

// Tool 是 agent 工具的契约实现
type Tool struct {
	launcher Launcher
}

// New 用给定的执行面构造 agent 工具
func New(launcher Launcher) *Tool {
	return &Tool{launcher: launcher}
}

func (t *Tool) Name() string { return "agent" }

func (t *Tool) Description() string {
	return agentToolDescription
}

func (t *Tool) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"description", "prompt"},
		"properties": map[string]any{
			"description": map[string]any{"type": "string", "description": "3-5 word summary of the task"},
			"prompt":      map[string]any{"type": "string", "description": "Task instructions. For a fresh agent this must be self-contained; for a fork it is a directive, not background."},
			"subagent_type": map[string]any{
				"type":        "string",
				"description": agentTypeEnumDescription(),
			},
			"worktree": map[string]any{"type": "boolean", "description": "Run the task in an isolated git worktree"},
			"thinking": map[string]any{"type": "string", "enum": llm.ThinkingLevelNames()},
			"model":    map[string]any{"type": "string", "description": "provider/model; ignored for forks"},
		},
	}
}

// agentTypeEnumDescription 把内置 agent 的用途说明拼成 subagent_type 的文档
func agentTypeEnumDescription() string {
	return "Omit to fork yourself and inherit this conversation. Or name an agent type — the built-ins are:\n" +
		"- explore: " + agentTypeDescriptions["explore"] + "\n" +
		"- plan: " + agentTypeDescriptions["plan"] + "\n" +
		"- work: " + agentTypeDescriptions["work"] + "\n" +
		"Custom agent names defined in .rivo/agent/agents/<name>.md or the user agent directory are also accepted."
}

func (t *Tool) PromptSnippet() string                { return agentToolSnippet }
func (t *Tool) PromptGuidelines() []string           { return agentToolGuidelines() }
func (t *Tool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

// Execute 解析工具参数并派发任务,返回启动信息
func (t *Tool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	if t.launcher == nil {
		return llm.ToolResult{}, errMissingLauncher
	}
	input, err := parseInput(args)
	if err != nil {
		return llm.ToolResult{}, err
	}
	// fork 子 agent 保留 Agent 工具只为工具定义一致,递归派发在调用边界拒绝
	if toolContext, ok := llm.CurrentToolContext(ctx); ok {
		input.ForkChild = toolContext.ForkChild
	}
	launched, err := t.launcher.Start(ctx, input)
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: fmt.Sprintf("Started subagent %s", launched.TaskID)}}, Details: launched}, nil
}
