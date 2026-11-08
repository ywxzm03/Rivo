// Package registry 是内置工具层的组合根：决定有哪些工具、
// 装配顺序与平台差异。编排层只接收装配结果，不直接依赖各工具子包
package registry

import (
	"runtime"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/subagent"
	"github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tool"
	"github.com/ywxzm03/Rivo/internal/tool/agenttool"
	"github.com/ywxzm03/Rivo/internal/tool/backgroundbash"
	"github.com/ywxzm03/Rivo/internal/tool/sendmessagetool"
	"github.com/ywxzm03/Rivo/internal/tool/taskstoptool"
)

// Deps 是装配内置工具所需的上层依赖
type Deps struct {
	WorkingDir     string
	SupportsImages bool
	// FileHistory 提供写类工具的备份追踪，可为 nil
	FileHistory tool.FileHistoryTracker
	// Store 是子任务状态存储，供后台 bash 与 task_stop 使用
	Store *task.Store
	// Launcher 供 agent 工具启动子任务
	Launcher agenttool.Launcher
	// Messenger 供 send_message 工具投递消息
	Messenger sendmessagetool.Messenger
}

// Build 返回内置工具集合，顺序即模型看到的工具顺序
func Build(deps Deps) []llm.Tool {
	tools := []llm.Tool{
		&tool.Read{SupportsImages: deps.SupportsImages, WorkingDir: deps.WorkingDir},
		&tool.Ls{WorkingDir: deps.WorkingDir},
		&tool.Grep{WorkingDir: deps.WorkingDir},
		&tool.Find{WorkingDir: deps.WorkingDir},
		&tool.Bash{WorkingDir: deps.WorkingDir, Background: backgroundbash.New(deps.Store)},
		&tool.Write{History: deps.FileHistory, WorkingDir: deps.WorkingDir},
		&tool.Edit{History: deps.FileHistory, WorkingDir: deps.WorkingDir},
		agenttool.New(deps.Launcher),
		sendmessagetool.New(deps.Messenger),
		taskstoptool.New(deps.Store),
	}
	// PowerShell 只在 Windows 上提供
	if runtime.GOOS == "windows" {
		tools = append(tools, &tool.PowerShell{WorkingDir: deps.WorkingDir})
	}
	return tools
}

// RunnerDeps 是子任务执行内核的装配依赖
type RunnerDeps struct {
	ParentCwd   string
	SessionDir  string
	AgentDir    string
	Thinking    llm.ThinkingLevel
	ParentTools []llm.Tool
	Store       *task.Store
	Model       llm.Model
	Stream      llm.StreamFn

	InputAccepted func(llm.UserInputOrigin)
}

// BuildRunner 构造子任务执行内核并把它挂到 manager 上，
// 让编排层不必知道子任务执行的装配细节
func BuildRunner(deps RunnerDeps, manager *subagent.Manager) *agenttool.Runner {
	runner := &agenttool.Runner{
		ParentCwd:     deps.ParentCwd,
		SessionDir:    deps.SessionDir,
		AgentDir:      deps.AgentDir,
		Thinking:      deps.Thinking,
		ParentTools:   deps.ParentTools,
		TaskStore:     deps.Store,
		InputAccepted: deps.InputAccepted,
	}
	runner.SetModel(deps.Model, deps.Stream)
	manager.SetFactory(runner.Run)
	manager.SetResumeResolver(agenttool.ResumeResolver(deps.SessionDir))
	return runner
}
