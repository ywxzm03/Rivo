package app

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tool"
)

type App struct {
	agent   *agent.Agent
	session *session.SessionManager
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
	app, err := newApp(cfg, session.Sink(sm, onEvent))
	if err != nil {
		return nil, err
	}
	app.session = sm
	return app, nil
}

// newApp 组装 Agent，onEvent 已包含会话持久化的 sink
func newApp(cfg Config, onEvent llm.AgentEventSink) (*App, error) {
	spec, err := lookupProvider(cfg.ProviderID)
	if err != nil {
		return nil, err
	}

	models, streamFn := spec.new(cfg.APIKey, cfg.BaseURL)
	model, err := findModel(spec.id, models, cfg.ModelID)
	if err != nil {
		return nil, err
	}

	tools := []llm.Tool{&tool.Read{SupportsImages: model.SupportsImages}, &tool.Ls{}, &tool.Grep{}, &tool.Find{}, &tool.Bash{}, &tool.Write{}, &tool.Edit{}}
	if runtime.GOOS == "windows" {
		tools = append(tools, &tool.PowerShell{})
	}

	return &App{
		agent: agent.NewAgent(
			llm.AgentConfig{Model: model},
			llm.Context{
				SystemPrompt: systemPrompt,
				Tools:        tools,
			},
			streamFn,
			onEvent,
		),
	}, nil
}

func (a *App) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	return a.agent.Run(ctx, input)
}
