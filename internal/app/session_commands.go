package app

import (
	"os"
	"path/filepath"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// newSession 准备独立会话并继承当前模型设置，失败时不替换旧 App
func (a *App) newSession() (*App, error) {
	model := a.CurrentModel()
	cfg := a.cfg
	cfg.ProviderID, cfg.ModelID = model.Provider, model.ID
	cfg.ThinkingLevel = a.CurrentThinkingLevel()
	if cfg.ThinkingLevel == "" {
		cfg.ThinkingLevel = llm.ThinkingLevelOff
	}
	// 1.新建运行实例会重新加载上下文资源，不携带旧对话或名称
	next, err := newApp(cfg, session.New(a.cwd, filepath.Dir(a.session.Path())), a.onEvent)
	if err != nil {
		return nil, err
	}
	// 2.初始设置写入隐藏草稿，命名或首条对话时与会话一起发布
	if err = next.session.AppendModelChange(model.Provider, model.ID); err == nil {
		err = next.session.AppendThinkingLevelChange(string(cfg.ThinkingLevel))
	}
	if err != nil {
		_ = os.Remove(next.session.Path() + ".draft")
		return nil, err
	}
	return next, nil
}
