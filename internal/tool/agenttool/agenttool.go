// Package agenttool 实现 agent 工具契约:把一次任务派发给异步 subagent
package agenttool

import (
	"context"
	"fmt"
	"strings"

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
	return "Launch an asynchronous subagent for an independent task."
}

func (t *Tool) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"description", "prompt"},
		"properties": map[string]any{
			"description":   map[string]any{"type": "string", "description": "Short task description"},
			"prompt":        map[string]any{"type": "string", "description": "Task instructions"},
			"name":          map[string]any{"type": "string"},
			"subagent_type": map[string]any{"type": "string"},
			"worktree":      map[string]any{"type": "boolean"},
			"thinking":      map[string]any{"type": "string", "enum": []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}},
			"model":         map[string]any{"type": "string"},
		},
	}
}

func (t *Tool) PromptSnippet() string                { return "Launch an asynchronous subagent" }
func (t *Tool) PromptGuidelines() []string           { return nil }
func (t *Tool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

// Execute 解析工具参数并派发任务,返回启动信息
func (t *Tool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	input := taskpkg.AgentInput{}
	// 1.解析必填字段与可选的展示名
	input.Description, _ = args["description"].(string)
	input.Prompt, _ = args["prompt"].(string)
	input.Name, _ = args["name"].(string)
	// 2.可选字段:未提供时保持 nil,让 Manager 走默认值
	if value, ok := args["subagent_type"].(string); ok && value != "" {
		input.SubagentType = &value
	}
	if value, ok := args["worktree"].(bool); ok {
		input.Worktree = &value
	}
	if value, ok := args["thinking"].(string); ok && value != "" {
		thinking := llm.ThinkingLevel(value)
		input.Thinking = &thinking
	}
	if value, ok := args["model"].(string); ok && value != "" {
		model := &llm.Model{ID: value}
		if parts := strings.SplitN(value, "/", 2); len(parts) == 2 {
			model.Provider, model.ID = parts[0], parts[1]
		}
		input.Model = model
	}
	launched, err := t.launcher.Start(ctx, input)
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: fmt.Sprintf("Started subagent %s", launched.AgentID)}}, Details: launched}, nil
}
