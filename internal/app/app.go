package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/subagent"
	"github.com/ywxzm03/Rivo/internal/tool"
)

type App struct {
	agent       *agent.Agent
	session     *session.SessionManager
	coordinator *Coordinator
	operationMu sync.Mutex
	// stateMu 串行化 operation 对模型配置、Agent 上下文和 session 投影的访问
	stateMu        sync.RWMutex
	active         *Operation
	operationID    uint64
	pendingRequest *OperationRequest
	cfg            Config
	onEvent        llm.AgentEventSink
	// loader 持有 AGENTS.md 链与 skills，Reload 时重读
	loader    *prompt.Loader
	cwd       string
	tools     []llm.Tool
	subagents *subagent.Manager
	// modelFallback 非空表示恢复时模型解析失败，已回退 env 默认模型
	modelFallback string
}

// ModelFallbackMessage 返回恢复时的模型回退警告，未发生回退时为空
func (a *App) ModelFallbackMessage() string {
	return a.modelFallback
}

func (a *App) Warnings() []string {
	if a.session == nil {
		return nil
	}
	return a.session.Warnings()
}

// Open 恢复指定会话文件：按投影记录的 Provider/Model 装配运行链，
// 模型解析失败时回退 env 配置并记录警告
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
	// 1.投影消息注入运行期上下文
	if messages := proj.Messages(); len(messages) > 0 {
		a.agent.ReplaceMessages(messages)
	}
	// 2.思考等级：仅在 settings 未指定时回填会话投影的显式记录
	if cfg.ThinkingLevel == "" && proj.HasThinkingLevel {
		a.agent.AgentConfig.ThinkingLevel = llm.ThinkingLevel(proj.ThinkingLevel)
	}
	a.modelFallback = warning
	// 3.恢复时对齐磁盘上的 AGENTS.md 与 skills
	a.Reload()
	return a, nil
}

// assembleResume 按投影模型装配运行链，解析失败回退 env 默认模型并返回回退警告
func assembleResume(cfg Config, proj compaction.ContextView, sm *session.SessionManager, onEvent llm.AgentEventSink) (*App, string, error) {
	if proj.Provider == "" || proj.Model == "" {
		a, err := newApp(cfg, sm, onEvent)
		return a, "", err
	}
	a, err := newAppWithModel(cfg, proj.Provider, proj.Model, sm, onEvent)
	if err == nil {
		return a, "", nil
	}
	a, err = newApp(cfg, sm, onEvent)
	if err != nil {
		return nil, "", err
	}
	return a, fallbackWarning(cfg, proj), nil
}

// fallbackWarning 拼回退警告文案；newApp 装配已成功，cfg 的默认模型必然有效
func fallbackWarning(cfg Config, proj compaction.ContextView) string {
	modelID := cfg.ModelID
	if modelID == "" {
		// 空 ModelID 经装配兜底为 provider 默认模型
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

// newAppWithModel 按指定厂商与模型装配编排层与 Agent：事件经编排层落盘，
// 压缩与溢出恢复由编排层驱动
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
	agentDir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	sm.AttachFileHistory(agentDir)

	manager := subagent.NewManager(subagent.NewStore(), nil)
	var notificationMu sync.Mutex
	var notificationBatch int
	tools := []llm.Tool{&tool.Read{SupportsImages: model.SupportsImages}, &tool.Ls{}, &tool.Grep{}, &tool.Find{}, &tool.Bash{Background: tool.NewBackgroundBashRunner(manager.Store())}, &tool.Write{History: sm.FileHistory()}, &tool.Edit{History: sm.FileHistory()}, manager.AgentTool(), manager.SendMessageTool(), manager.TaskStopTool()}
	if runtime.GOOS == "windows" {
		tools = append(tools, &tool.PowerShell{})
	}

	coord := NewCoordinator(sm, onEvent)
	coord.SetTransientMessages(func() []llm.Message {
		notifications := manager.Store().PeekNotifications()
		notificationMu.Lock()
		notificationBatch = len(notifications)
		notificationMu.Unlock()
		return subagentNotificationMessages(notifications)
	})
	coord.SetTransientAck(func() {
		notificationMu.Lock()
		count := notificationBatch
		notificationBatch = 0
		notificationMu.Unlock()
		manager.Store().AckNotifications(count)
	})
	coord.setModel(model)
	// 模型窗口已知时启用上下文压缩，摘要调用按默认策略重试瞬时错误
	if model.ContextWindow > 0 {
		coord.Compactor = &compaction.Compactor{StreamFn: streamFn, Model: model, Config: compaction.DefaultConfig(model.ContextWindow), Retry: compaction.DefaultRetryPolicy()}
	}

	// 1.资源加载：AGENTS.md 链与 skills 读盘一次，后续由 Reload 刷新
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("app: failed to get working directory: %w", err)
	}
	loader := prompt.NewLoader(cwd, agentDir)
	loader.Reload()

	// 2.基线分节交给编排层，请求前由它 diff 并落盘 system 消息
	coord.SetPromptSections(prompt.BuildSections(prompt.Options{
		Cwd:          cwd,
		Tools:        tools,
		ContextFiles: loader.ContextFiles(),
		Skills:       loader.Skills(),
	}))
	ag := agent.NewAgent(
		llm.AgentConfig{Model: model, ThinkingLevel: cfg.ThinkingLevel, SessionID: sm.ID()},
		llm.Context{
			Tools: tools,
		},
		streamFn,
		coord.Handle,
	)
	ag.WorkingDir = cwd
	ag.PrepareRequest = coord.PrepareRequest
	ag.CommitMessages = coord.CommitMessages
	coord.Agent = ag
	manager.SetFactory(newSubagentFactory(subagentFactoryConfig{
		ParentCwd:   cwd,
		SessionDir:  filepath.Dir(sm.Path()),
		AgentDir:    agentDir,
		ParentModel: model,
		Thinking:    cfg.ThinkingLevel,
		StreamFn:    streamFn,
		ParentTools: tools,
		TaskStore:   manager.Store(),
		OnEvent:     onEvent,
	}))
	manager.SetResumeResolver(subagentResumeResolver(filepath.Dir(sm.Path())))
	return &App{
		agent:       ag,
		session:     sm,
		coordinator: coord,
		cfg:         cfg,
		onEvent:     onEvent,
		loader:      loader,
		cwd:         cwd,
		tools:       tools,
		subagents:   manager,
	}, nil
}

func subagentNotificationMessages(notifications []subagent.TaskNotification) []llm.Message {
	if len(notifications) == 0 {
		return nil
	}
	result := make([]llm.Message, 0, len(notifications))
	for _, notification := range notifications {
		payload, err := json.Marshal(notification)
		if err != nil {
			continue
		}
		result = append(result, llm.UserMessage{Content: []llm.Content{llm.TextContent{
			Text: "<task-notification>\n" + string(payload) + "\n</task-notification>",
		}}})
	}
	return result
}

// SubagentStore exposes the root task store for TUI task projections and controls.
func (a *App) SubagentStore() *subagent.Store {
	if a.subagents == nil {
		return nil
	}
	return a.subagents.Store()
}

func (a *App) EnterSubagentView(taskID string) bool {
	store := a.SubagentStore()
	return store != nil && store.EnterView(taskID)
}

func (a *App) ExitSubagentView() {
	if store := a.SubagentStore(); store != nil {
		store.ExitView()
	}
}

func (a *App) EvictSubagent(taskID string) bool {
	store := a.SubagentStore()
	return store != nil && store.Evict(taskID)
}

func (a *App) StopSubagent(taskID string) error {
	store := a.SubagentStore()
	if store == nil {
		return fmt.Errorf("app: subagent manager is unavailable")
	}
	_, err := (&subagent.TaskStopTool{Store: store}).Execute(context.Background(), map[string]any{"task_id": taskID})
	return err
}

// ContinueSubagent resumes a terminal child while keeping its stable task ID.
func (a *App) ContinueSubagent(ctx context.Context, taskID, prompt string) (subagent.AgentToolLaunched, error) {
	if a.subagents == nil {
		return subagent.AgentToolLaunched{}, fmt.Errorf("app: subagent manager is unavailable")
	}
	return a.subagents.Continue(ctx, taskID, prompt)
}

// QueueSubagentMessage adds structured input to a running child FIFO.
func (a *App) QueueSubagentMessage(taskID string, message llm.UserMessage) error {
	if a.subagents == nil {
		return fmt.Errorf("app: subagent manager is unavailable")
	}
	return a.subagents.Store().EnqueuePendingMessage(taskID, message)
}

// Run 经编排层驱动一次运行
func (a *App) Run(ctx context.Context, input string) (*llm.AssistantMessage, error) {
	op, err := a.Start(ctx, OperationRequest{Kind: OperationRun, Value: input})
	if err != nil {
		return nil, err
	}
	result := op.Wait()
	return result.Reply, result.Err
}

// Compact 手动触发一次上下文压缩，instructions 为聚焦指令
func (a *App) Compact(ctx context.Context, instructions string) error {
	op, err := a.Start(ctx, OperationRequest{Kind: OperationCompact, Value: instructions})
	if err != nil {
		return err
	}
	return op.Wait().Err
}

// ContextView 返回带来源的已提交会话投影
func (a *App) ContextView() compaction.ContextView { return a.session.BuildContext() }

// Tree 返回当前会话的防御性树快照，供 fullscreen TUI 构建 user-only 选择器
func (a *App) Tree() session.TreeSnapshot { return a.session.Tree() }

func (a *App) SessionID() string { return a.session.ID() }

func (a *App) SessionName() string { return a.session.Name() }

func (a *App) TakeSteering() []string {
	if a == nil || a.agent == nil {
		return nil
	}
	return a.agent.TakeSteering()
}

func (a *App) TakeSteeringMessages() []llm.UserMessage {
	if a == nil || a.agent == nil {
		return nil
	}
	return a.agent.TakeSteeringMessages()
}

// Skills 返回当前已发现技能的副本
func (a *App) Skills() []prompt.Skill { return append([]prompt.Skill(nil), a.loader.Skills()...) }

func (a *App) CurrentModel() llm.Model { return a.agent.AgentConfig.Model }

func (a *App) CurrentThinkingLevel() llm.ThinkingLevel { return a.agent.AgentConfig.ThinkingLevel }

// SteeringMessages 返回当前尚未消费的 steering 文本，供显示层生成快照
func (a *App) SteeringMessages() []string {
	if a == nil || a.agent == nil {
		return nil
	}
	return a.agent.SteeringMessages()
}

func (a *App) SteeringUserMessages() []llm.UserMessage {
	if a == nil || a.agent == nil {
		return nil
	}
	return a.agent.SteeringUserMessages()
}

func (a *App) SteeringRevision() uint64 {
	if a == nil || a.agent == nil {
		return 0
	}
	return a.agent.SteeringRevision()
}

// AutoCompactionEnabled 表示当前模型是否启用了上下文压缩
func (a *App) AutoCompactionEnabled() bool {
	return a.coordinator != nil && a.coordinator.Compactor != nil
}

func (a *App) AvailableModels() []llm.Model { return a.cfg.AvailableModels() }

// SetThinkingLevel 更新当前模型的思考等级并记录会话设置
func (a *App) SetThinkingLevel(level llm.ThinkingLevel) error {
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationThinking, Value: string(level)})
	if err != nil {
		return err
	}
	return op.Wait().Err
}

func (a *App) setThinkingLevel(level llm.ThinkingLevel) error {
	if !validThinkingLevel(level) {
		return fmt.Errorf("unknown thinking level %q", level)
	}
	if err := a.session.AppendThinkingLevelChange(string(level)); err != nil {
		return err
	}
	a.agent.AgentConfig.ThinkingLevel = level
	return nil
}

// SetModel 切换模型、流适配器和压缩器
func (a *App) SetModel(providerID, modelID string) error {
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationModel, Value: providerID + "/" + modelID})
	if err != nil {
		return err
	}
	return op.Wait().Err
}

func (a *App) setModel(providerID, modelID string) error {
	return a.applyModel(providerID, modelID, true)
}

func (a *App) applyModel(providerID, modelID string, persist bool) error {
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
	if persist {
		if err := a.session.AppendModelChange(providerID, modelID); err != nil {
			return err
		}
	}
	a.agent.AgentConfig.Model = model
	a.coordinator.setModel(model)
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

// Reload 重读 AGENTS.md 与 skills 并重建基线分节，变更在下次请求前 diff 后追加
func (a *App) Reload() {
	a.loader.Reload()
	a.coordinator.SetPromptSections(prompt.BuildSections(prompt.Options{
		Cwd:          a.cwd,
		Tools:        a.tools,
		ContextFiles: a.loader.ContextFiles(),
		Skills:       a.loader.Skills(),
	}))
}
