package testutil

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// Tool 是跨包行为测试使用的可控工具
type Tool struct {
	ToolName        string                                                        // 暴露给脚本和 agent 的名称
	Mode            llm.ToolExecutionMode                                         // 批次调度方式，空值按 parallel
	Run             func(context.Context, map[string]any) (llm.ToolResult, error) // 受控的真实执行动作
	Schema          map[string]any                                                // 工具参数 schema
	DescriptionText string                                                        // 工具发现返回的描述
}

func (t *Tool) Name() string               { return t.ToolName }
func (t *Tool) Description() string        { return t.DescriptionText }
func (t *Tool) Parameters() map[string]any { return t.Schema }
func (t *Tool) PromptSnippet() string      { return "" }
func (t *Tool) PromptGuidelines() []string { return nil }
func (t *Tool) ExecutionMode() llm.ToolExecutionMode {
	if t.Mode == "" {
		return llm.ToolExecutionParallel
	}
	return t.Mode
}
func (t *Tool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	return t.Run(ctx, args)
}
