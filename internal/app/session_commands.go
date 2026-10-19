package app

import (
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
	next, err := newApp(cfg, session.New(a.cwd, filepath.Dir(a.session.Path())), a.onEvent)
	if err != nil {
		return nil, err
	}
	// 继承值仅初始化本次运行，恢复其他会话仍使用原启动配置的覆盖规则
	next.cfg = a.cfg
	if err = next.session.AppendModelChange(model.Provider, model.ID); err == nil {
		err = next.session.AppendThinkingLevelChange(string(cfg.ThinkingLevel))
	}
	if err != nil {
		return nil, err
	}
	return next, nil
}
