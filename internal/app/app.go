package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/subagent"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tool"
	"github.com/ywxzm03/Rivo/internal/tool/registry"
)

const (
	// evictInterval 是终态任务驱逐的轮询间隔,与面板宽限期同一量级
	evictInterval = time.Second
	// notificationRetryDelay 是通知唤醒被 operation 拒绝后的重试间隔。
	// 用户 run 可能持续很久,重试必须足够稀疏,只在槽位释放后尽快补投
	notificationRetryDelay = 250 * time.Millisecond
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
	cfg            provider.Config
	onEvent        llm.AgentEventSink
	// loader 持有 AGENTS.md 链与 skills，Reload 时重读
	loader *prompt.Loader
	cwd    string
	tools  []llm.Tool
	// subagents 是子任务管理器
	subagents *subagent.Manager
	// modelFallback 非空表示恢复时模型解析失败，已回退 env 默认模型
	modelFallback string
	// lifecycle 承载后台通知消费与终态任务驱逐两条循环
	lifecycle *lifecycle
	// backgroundEnd 是后台 run 结束的兜底回调,由显示层安装
	backgroundMu  sync.Mutex
	backgroundEnd func()
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
func Open(path string, cfg provider.Config, onEvent llm.AgentEventSink) (*App, error) {
	commandCwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("app: failed to get working directory: %w", err)
	}
	sm, err := session.Open(path, commandCwd)
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
		a.agent.SetThinkingLevel(llm.ThinkingLevel(proj.ThinkingLevel))
	}
	a.modelFallback = warning
	// 3.恢复时对齐磁盘上的 AGENTS.md 与 skills
	a.Reload()
	return a, nil
}

// assembleResume 按投影模型装配运行链，解析失败回退 env 默认模型并返回回退警告
func assembleResume(cfg provider.Config, proj ctxview.ContextView, sm *session.SessionManager, onEvent llm.AgentEventSink) (*App, string, error) {
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
func fallbackWarning(cfg provider.Config, proj ctxview.ContextView) string {
	modelID := cfg.ModelID
	if modelID == "" {
		// 空 ModelID 经装配兜底为 provider 默认模型
		modelID = cfg.DefaultModelID(cfg.ProviderID)
	}
	return fmt.Sprintf("Could not restore model %s/%s. Using %s/%s", proj.Provider, proj.Model, cfg.ProviderID, modelID)
}

// New 创建 App，会话文件写入默认目录 ~/.rivo/agent/sessions/
func New(cfg provider.Config, onEvent llm.AgentEventSink) (*App, error) {
	return NewWithSessionDir(cfg, "", onEvent)
}

func NewWithSessionDir(cfg provider.Config, dir string, onEvent llm.AgentEventSink) (*App, error) {
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

// NewWithSessionID 创建指定 ID 且延迟发布的会话
func NewWithSessionID(cfg provider.Config, id string, onEvent llm.AgentEventSink) (*App, error) {
	return NewWithSessionIDInDir(cfg, "", id, onEvent)
}

// NewWithSessionIDInDir 在指定目录创建稳定 ID 的会话
func NewWithSessionIDInDir(cfg provider.Config, dir, id string, onEvent llm.AgentEventSink) (*App, error) {
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
	sm, err := session.NewWithID(cwd, dir, id)
	if err != nil {
		return nil, err
	}
	return newApp(cfg, sm, onEvent)
}

// newApp 组装编排层与 Agent，模型取自 env 配置
func newApp(cfg provider.Config, sm *session.SessionManager, onEvent llm.AgentEventSink) (*App, error) {
	return newAppWithModel(cfg, cfg.ProviderID, cfg.ModelID, sm, onEvent)
}

// newAppWithModel 按指定厂商与模型装配编排层与 Agent：事件经编排层落盘，
// 压缩与溢出恢复由编排层驱动
func newAppWithModel(cfg provider.Config, providerID, modelID string, sm *session.SessionManager, onEvent llm.AgentEventSink) (*App, error) {
	resolved, err := cfg.Resolve(providerID, modelID)
	if err != nil {
		return nil, err
	}
	model, streamFn := resolved.Model, resolved.Stream
	agentDir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	sm.AttachFileHistory(agentDir)

	// 1.资源加载：会话恢复使用 header cwd，新会话使用当前进程 cwd
	cwd := sm.Cwd()
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("app: failed to get working directory: %w", err)
		}
	}

	manager := subagent.NewManager(taskpkg.NewStore(), nil)
	manager.Store().SetTranscriptReader(session.ReadMessages)
	tools := registry.Build(registry.Deps{
		WorkingDir:      cwd,
		SupportsImages:  model.SupportsImages,
		FileHistory:     tool.Tracker(sm.FileHistory()),
		Store:           manager.Store(),
		Launcher:        manager,
		Messenger:       manager,
		CodemodeEnabled: cfg.Codemode.Enabled == nil || *cfg.Codemode.Enabled,
		CodemodeHistory: sm,
	})

	coord := NewCoordinator(sm, onEvent)
	// 子任务通知由 agent 每轮开头认领并落盘进主会话,
	// 落盘成功才 ack:模型后续轮次仍能回看,不会"忘记"自己刚被告知的结果
	coord.SetNotificationClaimer(func() ([]llm.Message, func()) {
		claimed := manager.Store().ClaimNotifications()
		if claimed.Count == 0 {
			return nil, nil
		}
		ids := make([]string, 0, claimed.Count)
		for _, notification := range claimed.Notifications {
			ids = append(ids, notification.TaskID)
		}
		ack := func() {
			manager.Store().AckNotifications(claimed.Count)
			manager.Store().MarkNotified(ids)
		}
		return subagentNotificationMessages(claimed.Notifications), ack
	})
	// 每个请求前回收宽限期已过的终态子任务
	coord.SetTaskEvictor(manager.Store().EvictExpired)
	coord.setModel(model)
	// 模型窗口已知时启用上下文压缩，摘要调用按默认策略重试瞬时错误
	if model.ContextWindow > 0 {
		coord.Compactor = &compaction.Compactor{StreamFn: streamFn, Model: model, Config: compaction.DefaultConfig(model.ContextWindow), Retry: compaction.DefaultRetryPolicy()}
	}

	// 2.资源加载：AGENTS.md 链与 skills 读盘一次，后续由 Reload 刷新
	loader := prompt.NewLoader(cwd, agentDir)
	loader.Reload()

	// 3.基线分节交给编排层，请求前由它 diff 并落盘 system 消息
	coord.SetPromptSections(prompt.BuildSections(prompt.Options{
		Cwd:          cwd,
		Tools:        tools,
		ContextFiles: loader.ContextFiles(),
		Skills:       loader.Skills(),
	}))
	ag := agent.New(agent.Options{
		Config:     llm.AgentConfig{Model: model, ThinkingLevel: cfg.ThinkingLevel, SessionID: sm.ID()},
		Context:    llm.Context{Tools: tools},
		Stream:     streamFn,
		Events:     coord.Handle,
		WorkingDir: cwd,
		Hooks:      agent.Hooks{PrepareRequest: coord.PrepareRequest, CommitMessages: coord.CommitMessages},
	})
	coord.Agent = ag
	registry.BuildRunner(registry.RunnerDeps{
		ParentCwd:   cwd,
		SessionDir:  filepath.Dir(sm.Path()),
		AgentDir:    agentDir,
		Thinking:    cfg.ThinkingLevel,
		ParentTools: tools,
		Store:       manager.Store(),
		Model:       model,
		Stream:      streamFn,
	}, manager)
	app := &App{
		agent:       ag,
		session:     sm,
		coordinator: coord,
		cfg:         cfg,
		onEvent:     onEvent,
		loader:      loader,
		cwd:         cwd,
		subagents:   manager,
		tools:       tools,
		lifecycle:   newLifecycle(),
	}
	// 主 agent 空闲时靠这条回调消费通知;运行中的操作会在收尾阶段自行检查队列
	manager.Store().SetNotifyHook(app.lifecycle.wake)
	go app.lifecycle.runNotifications(app)
	// 终态任务的驱逐不能只靠请求边界:用户不再发消息时面板会一直留着旧任务行
	go app.lifecycle.runEviction(app)
	return app, nil
}

// SetEvictionInterval 覆盖驱逐轮询间隔，只对随后启动的循环生效
func (a *App) SetEvictionInterval(interval time.Duration) {
	a.lifecycle.setEvictionInterval(interval)
}

// notifyBackgroundRunEnd 通知显示层后台 run 已结束。
// 事件投影可能因为提交失败而漏发 AgentEndEvent,这里兜一次底
func (a *App) notifyBackgroundRunEnd() {
	a.backgroundMu.Lock()
	notify := a.backgroundEnd
	a.backgroundMu.Unlock()
	if notify != nil {
		notify()
	}
}

// SetBackgroundRunHook 安装后台 run 结束回调,由显示层接入
func (a *App) SetBackgroundRunHook(hook func()) {
	a.backgroundMu.Lock()
	a.backgroundEnd = hook
	a.backgroundMu.Unlock()
}

// subagentNotificationMessages 把待投递的子任务通知渲染成消息。
// 用 XML 标签而不是 JSON:模型按标签定位字段比解析 JSON 更稳,
// 空字段整段省略,避免模型把空串当成有效结果。
// 消息带 Hidden 标记:模型照常读到正文,TUI 投影与会话树跳过渲染
func subagentNotificationMessages(notifications []taskpkg.Notification) []llm.Message {
	if len(notifications) == 0 {
		return nil
	}
	result := make([]llm.Message, 0, len(notifications))
	for _, notification := range notifications {
		result = append(result, llm.UserMessage{Hidden: true, Content: []llm.Content{llm.TextContent{
			Text: renderNotification(notification),
		}}})
	}
	return result
}

// renderNotification 渲染单条 <task-notification>。
// summary 与 result 来自模型输出,必须转义后写入,否则会破坏标签结构
func renderNotification(notification taskpkg.Notification) string {
	var b strings.Builder
	b.WriteString(llm.TaskNotificationOpenTag + "\n")
	writeNotificationTag(&b, "task-id", notification.TaskID)
	writeNotificationTag(&b, "tool-use-id", notification.ToolUseID)
	writeNotificationTag(&b, "output-file", notification.OutputFile)
	writeNotificationTag(&b, "status", string(notification.Status))
	writeNotificationTag(&b, "reason", string(notification.Reason))
	writeNotificationTag(&b, "summary", notification.Summary)
	writeNotificationTag(&b, "result", notification.Result)
	// usage 与 worktree 是聚合段:任一子字段有值才输出整段
	if notification.TotalTokens > 0 || notification.ToolUses > 0 || notification.DurationMs > 0 {
		b.WriteString("<usage>")
		if notification.TotalTokens > 0 {
			fmt.Fprintf(&b, "<total_tokens>%d</total_tokens>", notification.TotalTokens)
		}
		if notification.ToolUses > 0 {
			fmt.Fprintf(&b, "<tool_uses>%d</tool_uses>", notification.ToolUses)
		}
		if notification.DurationMs > 0 {
			fmt.Fprintf(&b, "<duration_ms>%d</duration_ms>", notification.DurationMs)
		}
		b.WriteString("</usage>\n")
	}
	if notification.WorktreePath != "" {
		b.WriteString("<worktree>")
		writeNotificationTag(&b, "worktreePath", notification.WorktreePath)
		b.WriteString("</worktree>\n")
	}
	b.WriteString(llm.TaskNotificationCloseTag)
	return b.String()
}

// writeNotificationTag 只写有值的字段,空值整段省略
func writeNotificationTag(b *strings.Builder, tag, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "<%s>%s</%s>\n", tag, escapeNotificationText(value), tag)
}

// escapeNotificationText 转义会破坏标签结构的字符
func escapeNotificationText(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

// SubagentStore 暴露根任务存储，供 TUI 投影与操作
// subagentEvictionStore 返回驱逐循环需要的最小能力视图
func (a *App) subagentEvictionStore() subagentEvictor {
	store := a.SubagentStore()
	if store == nil {
		return nil
	}
	return store
}

func (a *App) SubagentStore() *taskpkg.Store {
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

// ShutdownSubagents 终止所有仍在运行的子任务与后台命令。
// 进程退出时调用:子任务 lifecycle 与请求解绑,不主动终止会变成孤儿进程
// Close 停掉后台循环并解除 store 的通知钩子。
// 会话切换时旧 App 必须调用它,否则通知循环与驱逐循环会永久存活
func (a *App) Close() {
	a.lifecycle.close()
	if store := a.SubagentStore(); store != nil {
		store.SetNotifyHook(nil)
	}
}

// startNotificationRun 用 Continue 驱动一次只带通知的空请求
func (a *App) startNotificationRun() (operationWaiter, error) {
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, NotificationOnly: true})
	if err != nil {
		return nil, err
	}
	return op, nil
}

func (a *App) ShutdownSubagents() int {
	a.Close()
	store := a.SubagentStore()
	if store == nil {
		return 0
	}
	return store.KillAllRunningTasks()
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
	_, err := taskpkg.StopTask(store, taskpkg.StopInput{TaskID: taskID})
	return err
}

// ContinueSubagent 用同一个稳定任务 ID 恢复终态子任务
func (a *App) ContinueSubagent(ctx context.Context, taskID, prompt string) (taskpkg.AgentLaunched, error) {
	if a.subagents == nil {
		return taskpkg.AgentLaunched{}, fmt.Errorf("app: subagent manager is unavailable")
	}
	return a.subagents.Continue(ctx, taskID, prompt)
}

// QueueSubagentMessage 把输入排进运行中子任务的队列。
// followUp 为真时进 follow-up 队列(当前轮结束后注入),否则进 steering 队列
func (a *App) QueueSubagentMessage(taskID string, message llm.UserMessage, followUp bool) error {
	if a.subagents == nil {
		return fmt.Errorf("app: subagent manager is unavailable")
	}
	if followUp {
		return a.subagents.Store().EnqueueFollowUpMessage(taskID, message)
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
func (a *App) ContextView() ctxview.ContextView { return a.session.BuildContext() }

// Tree 返回当前会话的防御性树快照，供 fullscreen TUI 构建 user-only 选择器
func (a *App) Tree() session.TreeSnapshot { return a.session.Tree() }

// Cwd 返回加载项目资源时使用的工作目录
func (a *App) Cwd() string { return a.cwd }

func (a *App) SessionID() string { return a.session.ID() }

func (a *App) SessionName() string { return a.session.Name() }

// SessionPath 返回会话 transcript 的绝对路径，尚未落盘时也是预定文件名
func (a *App) SessionPath() string { return a.session.Path() }

// Enqueue 把一条内容加入指定队列
func (a *App) Enqueue(kind agent.QueueKind, payload agent.PendingPayload) error {
	return a.agent.Enqueue(kind, payload)
}

// QueueLen 返回指定队列等待消费的条数
func (a *App) QueueLen(kind agent.QueueKind) int {
	return a.agent.Len(kind)
}

// QueueRevision 返回指定队列的修订号
func (a *App) QueueRevision(kind agent.QueueKind) uint64 {
	return a.agent.Revision(kind)
}

// QueueMessages 返回指定队列的纯文本视图
func (a *App) QueueMessages(kind agent.QueueKind) []string {
	return a.agent.Messages(kind)
}

// QueuePayloads 返回指定队列的完整内容视图
func (a *App) QueuePayloads(kind agent.QueueKind) []agent.PendingPayload {
	return a.agent.Payloads(kind)
}

// TakeQueue 取出并清空指定队列
func (a *App) TakeQueue(kind agent.QueueKind) []agent.PendingPayload {
	return a.agent.Take(kind)
}

// Skills 返回当前已发现技能的副本
func (a *App) Skills() []prompt.Skill { return append([]prompt.Skill(nil), a.loader.Skills()...) }

func (a *App) CurrentModel() llm.Model { return a.agent.Config().Model }

func (a *App) CurrentThinkingLevel() llm.ThinkingLevel { return a.agent.Config().ThinkingLevel }

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
	if level != "" && !llm.ValidThinkingLevel(level) {
		return fmt.Errorf("unknown effort level %q", level)
	}
	if err := a.session.AppendThinkingLevelChange(string(level)); err != nil {
		return err
	}
	a.agent.SetThinkingLevel(level)
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
	return a.switchModelPersisted(providerID, modelID)
}

// switchModel 切换模型但不写会话，用于临时切换与回滚
func (a *App) switchModel(providerID, modelID string) error {
	resolved, err := a.cfg.Resolve(providerID, modelID)
	if err != nil {
		return err
	}
	a.installModel(resolved)
	return nil
}

// switchModelPersisted 切换模型并把变更写入会话
func (a *App) switchModelPersisted(providerID, modelID string) error {
	resolved, err := a.cfg.Resolve(providerID, modelID)
	if err != nil {
		return err
	}
	if err := a.session.AppendModelChange(resolved.ProviderID, modelID); err != nil {
		return err
	}
	a.installModel(resolved)
	return nil
}

// installModel 把已解析的模型装到 agent 与压缩器上
func (a *App) installModel(resolved provider.Resolved) {
	model, streamFn := resolved.Model, resolved.Stream
	a.agent.SetModel(model)
	a.coordinator.setModel(model)
	a.agent.Reconfigure(a.agent.Config(), streamFn)
	for _, candidate := range a.agent.Context().Tools {
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
