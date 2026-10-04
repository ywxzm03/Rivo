package app

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tool"
)

type App struct {
	agent       *agent.Agent
	session     *session.SessionManager
	coordinator *session.Coordinator
	// modelFallback 非空表示恢复时投影模型解析失败，已回退 env 默认模型
	modelFallback string
}

// ModelFallbackMessage 返回恢复时的模型回退警告；未发生回退时为空
func (a *App) ModelFallbackMessage() string {
	return a.modelFallback
}

// Open 恢复指定会话文件：按投影记录的 Provider/Model 装配运行链，
// 模型解析失败时回退 env 配置并记录警告，投影无模型记录时直接走 env 配置
func Open(path string, cfg Config, onEvent llm.AgentEventSink) (*App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("app: 获取工作目录失败: %w", err)
	}
	sm, err := session.Open(path, cwd)
	if err != nil {
		return nil, err
	}
	proj := sm.BuildContext()
	if proj.Provider == "" || proj.Model == "" {
		return newApp(cfg, sm, onEvent)
	}
	a, err := newAppWithModel(cfg, proj.Provider, proj.Model, sm, onEvent)
	if err == nil {
		return a, nil
	}
	// 1.投影模型解析失败回退 env 默认模型，env 也失败按启动失败处理
	spec, err := lookupProvider(cfg.ProviderID)
	if err != nil {
		return nil, err
	}
	modelID := cfg.ModelID
	if modelID == "" {
		modelID = spec.defaultModel
	}
	a, err = newApp(cfg, sm, onEvent)
	if err != nil {
		return nil, err
	}
	a.modelFallback = fmt.Sprintf("Could not restore model %s/%s. Using %s/%s", proj.Provider, proj.Model, spec.id, modelID)
	return a, nil
}

// New 创建 App，会话文件写入默认目录 ~/.rivo/agent/sessions/
func New(cfg Config, onEvent llm.AgentEventSink) (*App, error) {
	return NewWithSessionDir(cfg, "", onEvent)
}

// NewWithSessionDir 创建 App 并把会话文件写入指定目录；dir 为空时使用默认会话目录
func NewWithSessionDir(cfg Config, dir string, onEvent llm.AgentEventSink) (*App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("app: 获取工作目录失败: %w", err)
	}
	if dir == "" {
		dir, err = session.DefaultDir(cwd)
		if err != nil {
			return nil, err
		}
	}
	sm := session.New(cwd, dir)
	return newApp(cfg, sm, onEvent)
}

// newApp 组装编排层与 Agent，模型取自 env 配置
func newApp(cfg Config, sm *session.SessionManager, onEvent llm.AgentEventSink) (*App, error) {
	return newAppWithModel(cfg, cfg.ProviderID, cfg.ModelID, sm, onEvent)
}

// newAppWithModel 按指定厂商与模型装配编排层与 Agent：事件经编排层落盘，压缩与溢出恢复由编排层驱动
func newAppWithModel(cfg Config, providerID, modelID string, sm *session.SessionManager, onEvent llm.AgentEventSink) (*App, error) {
	spec, err := lookupProvider(providerID)
	if err != nil {
		return nil, err
	}

	models, streamFn := spec.new(cfg.APIKey, cfg.BaseURL)
	model, err := findModel(spec.id, models, modelID)
	if err != nil {
		return nil, err
	}

	tools := []llm.Tool{&tool.Read{SupportsImages: model.SupportsImages}, &tool.Ls{}, &tool.Grep{}, &tool.Find{}, &tool.Bash{}, &tool.Write{}, &tool.Edit{}}
	if runtime.GOOS == "windows" {
		tools = append(tools, &tool.PowerShell{})
	}

	coord := session.NewCoordinator(sm, onEvent)
	// 模型窗口已知时启用上下文压缩，摘要调用按默认策略重试瞬时错误
	if model.ContextWindow > 0 {
		coord.Compactor = &compaction.Compactor{StreamFn: streamFn, Model: model, Config: compaction.DefaultConfig(model.ContextWindow), Retry: compaction.DefaultRetryPolicy()}
	}

	ag := agent.NewAgent(
		llm.AgentConfig{Model: model},
		llm.Context{
			SystemPrompt: systemPrompt,
			Tools:        tools,
		},
		streamFn,
		coord.Handle,
	)
	ag.Coordinator = coord
	coord.Agent = ag
	return &App{agent: ag, session: sm, coordinator: coord}, nil
}

// Run 经编排层驱动一次运行，溢出恢复由编排层负责
func (a *App) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	return a.coordinator.Run(ctx, input)
}

// Compact 手动触发一次上下文压缩，instructions 为聚焦指令
func (a *App) Compact(ctx context.Context, instructions string) error {
	return a.coordinator.Compact(ctx, instructions)
}
