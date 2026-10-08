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
	cfg         Config
	onEvent     llm.AgentEventSink
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
		return nil, fmt.Errorf("app: failed to get working directory: %w", err)
	}
	sm, err := session.Open(path, cwd)
	if err != nil {
		return nil, err
	}
	proj := sm.BuildContext()
	a, warning, err := assembleResume(cfg, proj, sm, onEvent)
	if err != nil {
		return nil, err
	}
	// 1.投影消息注入运行期上下文，恢复的会话才真正接上历史
	if len(proj.Messages) > 0 {
		a.agent.ReplaceMessages(proj.Messages)
	}
	// 2.思考等级：newAppWithModel 已按 cfg.ThinkingLevel 装配，
	// 仅在 settings 未指定时回填会话投影的显式记录（含 off）
	if cfg.ThinkingLevel == "" && proj.HasThinkingLevel {
		a.agent.AgentConfig.ThinkingLevel = llm.ThinkingLevel(proj.ThinkingLevel)
	}
	a.modelFallback = warning
	return a, nil
}

// assembleResume 按投影模型装配运行链，解析失败回退 env 默认模型并返回回退警告
func assembleResume(cfg Config, proj session.SessionContext, sm *session.SessionManager, onEvent llm.AgentEventSink) (*App, string, error) {
	if proj.Provider == "" || proj.Model == "" {
		a, err := newApp(cfg, sm, onEvent)
		return a, "", err
	}
	a, err := newAppWithModel(cfg, proj.Provider, proj.Model, sm, onEvent)
	if err == nil {
		return a, "", nil
	}
	// 1.投影模型解析失败回退 env 默认模型，env 也失败按启动失败处理
	a, err = newApp(cfg, sm, onEvent)
	if err != nil {
		return nil, "", err
	}
	return a, fallbackWarning(cfg, proj), nil
}

// fallbackWarning 拼回退警告文案；newApp 装配已成功，cfg 的默认模型必然有效
func fallbackWarning(cfg Config, proj session.SessionContext) string {
	modelID := cfg.ModelID
	if modelID == "" {
		// 空 ModelID 经装配兜底为 provider 默认模型，文案与实际装配保持一致
		if spec, err := cfg.lookupProvider(cfg.ProviderID); err == nil {
			modelID = spec.defaultModel
		}
	}
	return fmt.Sprintf("Could not restore model %s/%s. Using %s/%s", proj.Provider, proj.Model, cfg.ProviderID, modelID)
}

// New 创建 App，会话文件写入默认目录 ~/.rivo/agent/sessions/
func New(cfg Config, onEvent llm.AgentEventSink) (*App, error) {
	return NewWithSessionDir(cfg, "", onEvent)
}

// NewWithSessionDir 创建 App 并把会话文件写入指定目录；dir 为空时使用默认会话目录
func NewWithSessionDir(cfg Config, dir string, onEvent llm.AgentEventSink) (*App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("app: failed to get working directory: %w", err)
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
	spec, err := cfg.lookupProvider(providerID)
	if err != nil {
		return nil, err
	}

	models, streamFn := spec.new(spec.apiKey, spec.baseURL)
	if modelID == "" {
		modelID = spec.defaultModel
	}
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
		llm.AgentConfig{Model: model, ThinkingLevel: cfg.ThinkingLevel},
		llm.Context{
			SystemPrompt: systemPrompt,
			Tools:        tools,
		},
		streamFn,
		coord.Handle,
	)
	ag.Coordinator = coord
	coord.Agent = ag
	return &App{agent: ag, session: sm, coordinator: coord, cfg: cfg, onEvent: onEvent}, nil
}

// Run 经编排层驱动一次运行，溢出恢复由编排层负责
func (a *App) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	return a.coordinator.Run(ctx, input)
}

// Compact 手动触发一次上下文压缩，instructions 为聚焦指令
func (a *App) Compact(ctx context.Context, instructions string) error {
	return a.coordinator.Compact(ctx, instructions)
}

// SessionContext 返回当前会话的显示/模型投影副本
func (a *App) SessionContext() session.SessionContext {
	proj := a.session.BuildContext()
	proj.Messages = append([]llm.Message(nil), proj.Messages...)
	proj.EntryIDs = append([]string(nil), proj.EntryIDs...)
	return proj
}

// CurrentModel 返回当前运行模型副本
func (a *App) CurrentModel() llm.Model { return a.agent.AgentConfig.Model }

// CurrentThinkingLevel 返回当前思考等级
func (a *App) CurrentThinkingLevel() llm.ThinkingLevel { return a.agent.AgentConfig.ThinkingLevel }

// AutoCompactionEnabled 表示当前模型是否启用了上下文压缩
func (a *App) AutoCompactionEnabled() bool {
	return a.coordinator != nil && a.coordinator.Compactor != nil
}

// AvailableModels 返回当前配置中的模型目录
func (a *App) AvailableModels() []llm.Model { return a.cfg.AvailableModels() }

// SetThinkingLevel 更新当前模型的思考等级并记录会话设置
func (a *App) SetThinkingLevel(level llm.ThinkingLevel) error {
	if !validThinkingLevel(level) {
		return fmt.Errorf("unknown thinking level %q", level)
	}
	a.agent.AgentConfig.ThinkingLevel = level
	return a.session.AppendThinkingLevelChange(string(level))
}

// SetModel 切换模型、流适配器和压缩器；校验完成后才应用运行模型
func (a *App) SetModel(providerID, modelID string) error {
	spec, err := a.cfg.lookupProvider(providerID)
	if err != nil {
		return err
	}
	providerID = spec.id
	models, streamFn := spec.new(spec.apiKey, spec.baseURL)
	model, err := findModel(spec.id, models, modelID)
	if err != nil {
		for _, candidate := range a.AvailableModels() {
			if candidate.Provider == providerID && candidate.ID == modelID {
				model, err = candidate, nil
				break
			}
		}
		if err != nil {
			return err
		}
	}
	if err := a.session.AppendModelChange(providerID, modelID); err != nil {
		return err
	}
	a.agent.AgentConfig.Model = model
	a.agent.StreamFn = streamFn
	for _, candidate := range a.agent.AgentContext.Tools {
		if read, ok := candidate.(*tool.Read); ok {
			read.SupportsImages = model.SupportsImages
		}
	}
	if model.ContextWindow > 0 {
		if a.coordinator.Compactor == nil {
			a.coordinator.Compactor = &compaction.Compactor{Config: compaction.DefaultConfig(model.ContextWindow), Retry: compaction.DefaultRetryPolicy()}
		}
		a.coordinator.Compactor.Model = model
		a.coordinator.Compactor.StreamFn = streamFn
		a.coordinator.Compactor.Config = compaction.DefaultConfig(model.ContextWindow)
	} else {
		a.coordinator.Compactor = nil
	}
	return nil
}

func validThinkingLevel(level llm.ThinkingLevel) bool {
	switch level {
	case llm.ThinkingLevelOff, llm.ThinkingLevelMinimal, llm.ThinkingLevelLow, llm.ThinkingLevelMedium,
		llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh, llm.ThinkingLevelMax:
		return true
	default:
		return false
	}
}
