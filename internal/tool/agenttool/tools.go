package agenttool

import (
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	toolpkg "github.com/ywxzm03/Rivo/internal/tool"
	"github.com/ywxzm03/Rivo/internal/tool/backgroundbash"
)

// childTools 构造子 agent 的工具集。
// fork 复用父的 agent 与 TaskStop 实例,写类工具换绑子 transcript 的实例
func childTools(parent []llm.Tool, fork bool, childSession *session.SessionManager, taskStore *taskpkg.Store) []llm.Tool {
	tools := make([]llm.Tool, 0, len(parent))
	for _, tool := range parent {
		// 1.写类工具必须换实例,否则子 agent 的改动会进入父会话的 undo 快照
		switch tool.(type) {
		case *toolpkg.Write:
			tools = append(tools, &toolpkg.Write{History: toolpkg.Tracker(childSession.FileHistory())})
			continue
		case *toolpkg.Edit:
			tools = append(tools, &toolpkg.Edit{History: toolpkg.Tracker(childSession.FileHistory())})
			continue
		case *toolpkg.Bash:
			tools = append(tools, &toolpkg.Bash{Background: backgroundbash.New(taskStore)})
			continue
		}
		// 2.fork 复用父的 agent 工具;typed subagent 不能继续派发,移除它
		if tool.Name() == "agent" && !fork {
			continue
		}
		tools = append(tools, tool)
	}
	return tools
}

// readOnlyTools 保留只读工具。bash 保持完整能力,只读性由工具池裁剪与 prompt 约束保证,
// 与 Claude Code 的 explore/plan 一致。TaskStop 必须保留:子 agent 能用 bash 起后台任务,
// 就必须能停止它,否则会留下无法回收的孤儿进程
func readOnlyTools(parent []llm.Tool) []llm.Tool {
	tools := make([]llm.Tool, 0, len(parent))
	for _, tool := range parent {
		switch tool.Name() {
		case "write", "edit", "powershell", "agent":
			continue
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
