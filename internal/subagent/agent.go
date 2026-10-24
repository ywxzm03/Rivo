package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type AgentTaskSpec struct {
	ID              string
	Description     string
	Prompt          string
	AgentType       string
	ToolUseID       string
	OutputFile      string
	WorktreePath    string
	Worktree        bool
	PendingMessages []llm.UserMessage
	InitialMessages []llm.Message
	Model           llm.Model
	Thinking        llm.ThinkingLevel
	Continuation    bool
	ParentContext   *llm.Context
	ParentConfig    *llm.AgentConfig
}

type AgentRuntimeFactory func(context.Context, AgentTaskSpec) (*AgentRuntime, error)

type Manager struct {
	store          *Store
	factory        AgentRuntimeFactory
	continuationMu sync.Mutex
	resumeResolver ResumeResolver
}

func NewManager(store *Store, factory AgentRuntimeFactory) *Manager {
	if store == nil {
		store = NewStore()
	}
	return &Manager{store: store, factory: factory}
}

func (m *Manager) Store() *Store { return m.store }

func (m *Manager) SetFactory(factory AgentRuntimeFactory) { m.factory = factory }

// SetResumeResolver 安装磁盘查找器,供 SendMessage 定位已不在 store 中的任务
func (m *Manager) SetResumeResolver(resolver ResumeResolver) { m.resumeResolver = resolver }

func (m *Manager) TaskStopTool() *TaskStopTool { return &TaskStopTool{Store: m.store} }

func (m *Manager) AgentTool() *AgentTool { return &AgentTool{manager: m} }

func (m *Manager) Start(ctx context.Context, input AgentToolInput) (AgentToolLaunched, error) {
	if strings.TrimSpace(input.Description) == "" || strings.TrimSpace(input.Prompt) == "" {
		return AgentToolLaunched{}, errors.New("agent: description and prompt are required")
	}
	if m.factory == nil {
		return AgentToolLaunched{}, errors.New("agent: runtime factory is unavailable")
	}
	agentType := "fork"
	if input.SubagentType != nil && strings.TrimSpace(*input.SubagentType) != "" {
		agentType = strings.TrimSpace(*input.SubagentType)
	}
	switch strings.ToLower(agentType) {
	case "explore", "plan", "work":
		agentType = strings.ToLower(agentType)
	case "general", "general-purpose":
		// 接受源侧名称作为别名,但任务状态和 TUI 只暴露 Rivo 的 work 内置类型
		agentType = "work"
	}
	if strings.EqualFold(agentType, "fork") {
		agentType = "fork"
	}
	stateID, _, err := m.store.Register(RegisterSpec{
		Type:        TaskTypeAgent,
		Description: input.Description,
		AgentType:   agentType,
		Model:       input.Model,
		Thinking:    thinkingValue(input.Thinking),
		Worktree:    input.Worktree != nil && *input.Worktree,
	})
	if err != nil {
		return AgentToolLaunched{}, err
	}
	// 首次注册先占住稳定 ID,runtime 在 lifecycle 能发布任何终态回调之前挂上
	toolContext, hasToolContext := llm.CurrentToolContext(ctx)
	spec := AgentTaskSpec{ID: stateID, Description: input.Description, Prompt: input.Prompt, AgentType: agentType, Model: modelValue(input.Model), Thinking: thinkingValue(input.Thinking), Worktree: input.Worktree != nil && *input.Worktree}
	if hasToolContext {
		spec.ParentContext = &toolContext.Context
		spec.ParentConfig = &toolContext.Config
	}
	runtime, err := m.factory(ctx, spec)
	if err != nil {
		_ = m.store.failReserved(stateID, err)
		return AgentToolLaunched{}, err
	}
	if runtime == nil || runtime.Agent == nil {
		_ = m.store.failReserved(stateID, errors.New("agent: factory returned no agent"))
		return AgentToolLaunched{}, errors.New("agent: factory returned no agent")
	}
	if runtime.Done == nil {
		runtime.Done = make(chan struct{})
	}
	// Agent 工具是显式异步的。工具批次在返回结果后就会取消其子 context,
	// 因此要把任务 lifecycle 与该短命请求 context 解绑。TaskStop 仍是子任务的
	// 显式取消边界。
	lifecycleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	runtime.Cancel = cancel
	token, err := m.store.attachAgentRuntime(stateID, runtime, input.Prompt)
	if err != nil {
		cancel()
		return AgentToolLaunched{}, err
	}
	m.store.setRuntimeMetadata(stateID, token, runtime)
	m.installPendingDrain(stateID, token, runtime)
	go m.runAgent(lifecycleCtx, stateID, token, runtime, input)
	launched := AgentToolLaunched{Status: "async_launched", AgentID: stateID, Description: input.Description, Prompt: input.Prompt, SubagentType: agentType, State: StateRunning, OutputFile: runtime.OutputFile, WorktreePath: runtime.WorktreePath}
	return launched, nil
}

// Continue 用同一个稳定 agent ID 恢复终态任务。旧 lifecycle 先取消并完全 drain,
// 然后才挂上新 runtime。
func (m *Manager) Continue(ctx context.Context, taskID, prompt string) (AgentToolLaunched, error) {
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(prompt) == "" {
		return AgentToolLaunched{}, errors.New("agent: task ID and prompt are required")
	}
	if m.factory == nil {
		return AgentToolLaunched{}, errors.New("agent: runtime factory is unavailable")
	}
	m.continuationMu.Lock()
	defer m.continuationMu.Unlock()

	task, ok := m.store.Task(taskID)
	if !ok {
		return AgentToolLaunched{}, &StopTaskError{Code: "not_found", Message: fmt.Sprintf("No task found with ID: %s", taskID)}
	}
	if task.Base().Status == StateRunning {
		return AgentToolLaunched{}, errors.New("agent: task is still running; queue input instead")
	}
	oldRuntime, _, err := m.store.continuationRuntime(taskID)
	if err != nil {
		return AgentToolLaunched{}, err
	}
	if oldRuntime != nil {
		if oldRuntime.Cancel != nil {
			oldRuntime.Cancel()
		}
		if oldRuntime.Done != nil {
			select {
			case <-oldRuntime.Done:
			case <-ctx.Done():
				return AgentToolLaunched{}, ctx.Err()
			}
		}
	}

	agentTask, ok := task.(*AgentTaskState)
	if !ok {
		return AgentToolLaunched{}, &StopTaskError{Code: "unsupported_type", Message: fmt.Sprintf("Task %s is not an agent task", taskID)}
	}
	agentType := agentTask.AgentType
	runtime, err := m.factory(ctx, AgentTaskSpec{
		ID: taskID, Description: task.Base().Description, Prompt: prompt,
		AgentType: agentType, OutputFile: task.Base().OutputFile,
		WorktreePath:    agentTask.WorktreePath,
		Worktree:        agentTask.Worktree,
		PendingMessages: cloneUserMessages(agentTask.PendingMessages),
		Model:           modelFromState(agentTask), Thinking: agentTask.Thinking, Continuation: true,
	})
	if err != nil {
		return AgentToolLaunched{}, err
	}
	if runtime == nil || runtime.Agent == nil {
		return AgentToolLaunched{}, errors.New("agent: factory returned no agent")
	}
	if runtime.Done == nil {
		runtime.Done = make(chan struct{})
	}
	lifecycleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	runtime.Cancel = cancel
	token, err := m.store.RegisterContinuation(taskID, runtime, prompt)
	if err != nil {
		cancel()
		return AgentToolLaunched{}, err
	}
	m.store.setRuntimeMetadata(taskID, token, runtime)
	m.installPendingDrain(taskID, token, runtime)
	go m.runAgent(lifecycleCtx, taskID, token, runtime, AgentToolInput{
		Description: task.Base().Description, Prompt: prompt,
		SubagentType: &agentType,
	})
	return AgentToolLaunched{Status: "async_launched", AgentID: taskID, Description: task.Base().Description, Prompt: prompt, SubagentType: agentType, State: StateRunning, OutputFile: runtime.OutputFile, WorktreePath: runtime.WorktreePath}, nil
}

func modelFromState(task *AgentTaskState) llm.Model {
	if task.Model == nil {
		return llm.Model{}
	}
	return *task.Model
}

func modelValue(model *llm.Model) llm.Model {
	if model == nil {
		return llm.Model{}
	}
	return *model
}

func thinkingValue(thinking *llm.ThinkingLevel) llm.ThinkingLevel {
	if thinking == nil {
		return ""
	}
	return *thinking
}

func (m *Manager) runAgent(ctx context.Context, taskID string, token uint64, runtime *AgentRuntime, input AgentToolInput) {
	started := time.Now()
	defer func() {
		if runtime.Cancel != nil {
			runtime.Cancel()
		}
	}()
	run := runtime.Run
	if run == nil {
		run = func(runCtx context.Context, prompt string) (*llm.AssistantMessage, error) {
			return runtime.Agent.RunMessage(runCtx, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: prompt}}})
		}
	}
	reply, runErr := run(ctx, input.Prompt)
	m.store.RecordAgentMessages(taskID, token, runtime.Agent.AgentContext.Messages)
	wasKilled := false
	if task, ok := m.store.Task(taskID); ok {
		wasKilled = task.Base().LifecycleToken == token && task.Base().Status == StateKilled
	}
	if wasKilled || errors.Is(ctx.Err(), context.Canceled) || errors.Is(runErr, context.Canceled) {
		partial := latestAssistant(runtime.Agent)
		if task, ok := m.store.Task(taskID); ok && task.Base().Status == StateRunning {
			_ = AgentTask{}.Kill(taskID, m.store)
		}
		partialResult := agentResult(taskID, input, partial, time.Since(started))
		notification := m.notification(taskID, token, TaskNotification{TaskID: taskID, Status: StateKilled, Summary: input.Description, Result: firstText(partialResult.Content), TotalTokens: partialResult.TotalTokens, ToolUses: partialResult.TotalToolUseCount, DurationMs: time.Since(started).Milliseconds()})
		m.cleanupWorktree(taskID, token, runtime)
		m.store.EnqueueNotification(taskID, token, notification)
	} else if runErr != nil {
		transitioned := m.store.FailAgent(taskID, token, runErr)
		notification := m.notification(taskID, token, TaskNotification{TaskID: taskID, Status: StateFailed, Summary: runErr.Error(), DurationMs: time.Since(started).Milliseconds()})
		m.cleanupWorktree(taskID, token, runtime)
		if transitioned {
			m.store.EnqueueNotification(taskID, token, notification)
		} else if task, ok := m.store.Task(taskID); ok && task.Base().LifecycleToken == token && task.Base().Status == StateKilled {
			m.store.EnqueueNotification(taskID, token, m.notification(taskID, token, TaskNotification{TaskID: taskID, Status: StateKilled, Summary: input.Description, DurationMs: time.Since(started).Milliseconds()}))
		}
	} else {
		result := agentResult(taskID, input, reply, time.Since(started))
		transitioned := m.store.CompleteAgent(taskID, token, &result)
		notification := m.notification(taskID, token, TaskNotification{TaskID: taskID, Status: StateCompleted, Summary: input.Description, Result: firstText(result.Content), TotalTokens: result.TotalTokens, DurationMs: result.TotalDurationMs, ToolUses: result.TotalToolUseCount})
		m.cleanupWorktree(taskID, token, runtime)
		if transitioned {
			m.store.EnqueueNotification(taskID, token, notification)
		}
	}
	m.store.removeRuntime(taskID, token)
	close(runtime.Done)
}

func (m *Manager) cleanupWorktree(taskID string, token uint64, runtime *AgentRuntime) {
	if runtime == nil || runtime.WorktreeCleanup == nil {
		return
	}
	if runtime.WorktreeCleanup() {
		m.store.ClearWorktreePath(taskID, token)
	}
}

func latestAssistant(runtime *agent.Agent) *llm.AssistantMessage {
	if runtime == nil {
		return nil
	}
	for i := len(runtime.AgentContext.Messages) - 1; i >= 0; i-- {
		if message, ok := runtime.AgentContext.Messages[i].(llm.AssistantMessage); ok {
			copy := message
			return &copy
		}
	}
	return nil
}

func (m *Manager) notification(taskID string, token uint64, notification TaskNotification) TaskNotification {
	task, ok := m.store.Task(taskID)
	if !ok || task.Base().LifecycleToken != token {
		return notification
	}
	notification.ToolUseID = task.Base().ToolUseID
	notification.OutputFile = task.Base().OutputFile
	if agentTask, ok := task.(*AgentTaskState); ok {
		notification.WorktreePath = agentTask.WorktreePath
	}
	return notification
}

func (m *Manager) installPendingDrain(taskID string, token uint64, runtime *AgentRuntime) {
	previous := runtime.Agent.OnEvent
	runtime.Agent.OnEvent = func(event llm.AgentEvent) error {
		if previous != nil {
			if err := previous(event); err != nil {
				return err
			}
		}
		// 只发布已提交的消息边界。若每个流式 delta 都复制完整子 context,
		// 子任务继承大 transcript 时会产生平方级分配压力。
		switch event.(type) {
		case llm.MessageEndEvent, llm.TurnEndEvent:
			m.store.RecordAgentMessages(taskID, token, runtime.Agent.AgentContext.Messages)
		}
		if event.Type() != llm.AgentEventTurnEnd {
			m.updateProgress(taskID, token, event)
			return nil
		}
		m.updateProgress(taskID, token, event)
		messages := m.store.DrainPendingMessages(taskID, token)
		if len(messages) == 0 {
			return nil
		}
		for i, message := range messages {
			if err := runtime.Agent.SteerMessage(message); err != nil {
				m.store.RequeuePendingMessagesFront(taskID, token, messages[i:])
				return err
			}
		}
		return nil
	}
}

func (m *Manager) updateProgress(taskID string, token uint64, event llm.AgentEvent) {
	m.store.UpdateAgentProgress(taskID, token, func(progress *AgentProgress) {
		switch event := event.(type) {
		case llm.ToolExecutionStartEvent:
			progress.ToolUseCount++
			progress.LastActivity = &ToolActivity{ToolName: event.ToolName, ActivityDescription: event.ToolName}
			progress.RecentActivities = append(progress.RecentActivities, *progress.LastActivity)
			if len(progress.RecentActivities) > 5 {
				progress.RecentActivities = progress.RecentActivities[len(progress.RecentActivities)-5:]
			}
		case llm.MessageEndEvent:
			if message, ok := event.Message.(llm.AssistantMessage); ok {
				progress.TokenCount += message.Usage.OutputTokens
			}
		}
	})
}

func agentResult(taskID string, input AgentToolInput, reply *llm.AssistantMessage, duration time.Duration) AgentToolResult {
	result := AgentToolResult{AgentID: taskID, AgentType: "fork", TotalDurationMs: duration.Milliseconds()}
	if input.SubagentType != nil {
		result.AgentType = *input.SubagentType
	}
	if reply == nil {
		return result
	}
	result.Usage = reply.Usage
	result.TotalTokens = reply.Usage.TotalTokens
	for _, content := range reply.Content {
		if text, ok := content.(llm.TextContent); ok {
			result.Content = append(result.Content, TextBlock{Text: text.Text})
		}
		if _, ok := content.(llm.ToolCall); ok {
			result.TotalToolUseCount++
		}
	}
	return result
}

func firstText(blocks []TextBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	return blocks[0].Text
}

type AgentTool struct{ manager *Manager }

func (t *AgentTool) Name() string { return "agent" }
func (t *AgentTool) Description() string {
	return "Launch an asynchronous subagent for an independent task."
}
func (t *AgentTool) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"description", "prompt"},
		"properties": map[string]any{
			"description":   map[string]any{"type": "string", "description": "Short task description"},
			"prompt":        map[string]any{"type": "string", "description": "Task instructions"},
			"name":          map[string]any{"type": "string"},
			"subagent_type": map[string]any{"type": "string"},
			"worktree":      map[string]any{"type": "boolean"},
			"thinking":      map[string]any{"type": "string", "enum": []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}},
			"model":         map[string]any{"type": "string"},
		},
	}
}
func (t *AgentTool) PromptSnippet() string                { return "Launch an asynchronous subagent" }
func (t *AgentTool) PromptGuidelines() []string           { return nil }
func (t *AgentTool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (t *AgentTool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	input := AgentToolInput{}
	input.Description, _ = args["description"].(string)
	input.Prompt, _ = args["prompt"].(string)
	input.Name, _ = args["name"].(string)
	if value, ok := args["subagent_type"].(string); ok && value != "" {
		input.SubagentType = &value
	}
	if value, ok := args["worktree"].(bool); ok {
		input.Worktree = &value
	}
	if value, ok := args["thinking"].(string); ok && value != "" {
		thinking := llm.ThinkingLevel(value)
		input.Thinking = &thinking
	}
	if value, ok := args["model"].(string); ok && value != "" {
		model := &llm.Model{ID: value}
		if parts := strings.SplitN(value, "/", 2); len(parts) == 2 {
			model.Provider, model.ID = parts[0], parts[1]
		}
		input.Model = model
	}
	launched, err := t.manager.Start(ctx, input)
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: fmt.Sprintf("Started subagent %s", launched.AgentID)}}, Details: launched}, nil
}
