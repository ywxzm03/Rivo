package agenttool

import (
	"context"
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	toolpkg "github.com/ywxzm03/Rivo/internal/tool"
)

// childTools 构造子 agent 的工具集:换掉会写父 transcript 的实例,并移除 TaskStop
func childTools(parent []llm.Tool, keepAgent bool, childSession *session.SessionManager, taskStore *taskpkg.Store) []llm.Tool {
	if taskStore == nil {
		taskStore = taskpkg.NewStore()
	}
	tools := make([]llm.Tool, 0, len(parent))
	for _, tool := range parent {
		// 1.TaskStop 只属于根 agent,子 agent 不得终止其他任务
		if tool.Name() == "TaskStop" {
			continue
		}
		if tool.Name() == "agent" && !keepAgent {
			continue
		}
		if tool.Name() == "agent" && keepAgent {
			tools = append(tools, rejectingAgentTool{Tool: tool})
			continue
		}
		// 2.写类工具要绑定子 transcript 的 file history
		switch tool.(type) {
		case *toolpkg.Write:
			tools = append(tools, &toolpkg.Write{History: childSession.FileHistory()})
			continue
		case *toolpkg.Edit:
			tools = append(tools, &toolpkg.Edit{History: childSession.FileHistory()})
			continue
		case *toolpkg.Bash:
			tools = append(tools, &toolpkg.Bash{Background: toolpkg.NewBackgroundBashRunner(taskStore)})
			continue
		}
		tools = append(tools, tool)
	}
	return tools
}

// readOnlyTools 保留只读工具,bash 降级为只读版本
func readOnlyTools(parent []llm.Tool) []llm.Tool {
	tools := make([]llm.Tool, 0, len(parent))
	for _, tool := range parent {
		switch tool.Name() {
		case "write", "edit", "powershell", "agent", "TaskStop":
			continue
		case "bash":
			if bash, ok := tool.(*toolpkg.Bash); ok {
				tools = append(tools, toolpkg.NewReadOnlyBash(bash))
			} else {
				tools = append(tools, tool)
			}
		default:
			tools = append(tools, tool)
		}
	}
	return tools
}

// filterAgentTools 按 agent 定义的 tools 列表过滤;请求了不存在的工具时报错
func filterAgentTools(parent []llm.Tool, allowed []string) ([]llm.Tool, error) {
	if len(allowed) == 0 {
		return parent, nil
	}
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		if strings.TrimSpace(name) == "*" {
			return parent, nil
		}
		set[strings.ToLower(name)] = true
	}
	result := make([]llm.Tool, 0, len(parent))
	for _, tool := range parent {
		if set[strings.ToLower(tool.Name())] {
			result = append(result, tool)
		}
	}
	for name := range set {
		found := false
		for _, tool := range result {
			if strings.EqualFold(tool.Name(), name) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("subagent: agent definition requests unknown or unavailable tool %q", name)
		}
	}
	return result, nil
}

// rejectingAgentTool 让 fork 子 agent 看到 agent 工具但拒绝嵌套调用
type rejectingAgentTool struct{ llm.Tool }

func (rejectingAgentTool) Execute(context.Context, map[string]any) (llm.ToolResult, error) {
	return llm.ToolResult{IsError: true, Content: []llm.Content{llm.TextContent{Text: "nested subagents are unavailable in a fork child"}}}, nil
}
