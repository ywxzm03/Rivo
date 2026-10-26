package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

type AgentRuntimeFactory func(context.Context, taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error)

type Manager struct {
	store          *taskpkg.Store
	factory        AgentRuntimeFactory
	continuationMu sync.Mutex
	resumeResolver taskpkg.ResumeResolver
}

func NewManager(store *taskpkg.Store, factory AgentRuntimeFactory) *Manager {
	return &Manager{store: store, factory: factory}
}

func (m *Manager) Store() *taskpkg.Store { return m.store }

func (m *Manager) SetFactory(factory AgentRuntimeFactory) { m.factory = factory }

// SetResumeResolver 安装磁盘查找器,供 SendMessage 定位已不在 store 中的任务
func (m *Manager) SetResumeResolver(resolver taskpkg.ResumeResolver) { m.resumeResolver = resolver }

func (m *Manager) Start(ctx context.Context, input taskpkg.AgentInput) (taskpkg.AgentLaunched, error) {
	if strings.TrimSpace(input.Description) == "" || strings.TrimSpace(input.Prompt) == "" {
		return taskpkg.AgentLaunched{}, errors.New("agent: description and prompt are required")
	}
	// fork 子 agent 保留 Agent 工具是为了让工具定义与父缓存一致,
	// 递归派发必须在这里拒绝,不能只靠 prompt 软约束
	if input.ForkChild {
		return taskpkg.AgentLaunched{}, errors.New("agent: fork is not available inside a forked worker; complete the task directly")
	}
	if m.factory == nil {
		return taskpkg.AgentLaunched{}, errors.New("agent: runtime factory is unavailable")
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
	stateID, token, err := m.store.Register(taskpkg.RegisterSpec{
		Type:        taskpkg.TypeAgent,
		Description: input.Description,
		AgentType:   agentType,
		Model:       input.Model,
		Thinking:    thinkingValue(input.Thinking),
		Worktree:    input.Worktree != nil && *input.Worktree,
	})
	if err != nil {
		return taskpkg.AgentLaunched{}, err
	}
	// 首次注册先占住稳定 ID,runtime 在 lifecycle 能发布任何终态回调之前挂上
	toolContext, hasToolContext := llm.CurrentToolContext(ctx)
	spec := taskpkg.AgentSpec{ID: stateID, Description: input.Description, Prompt: input.Prompt, AgentType: agentType, Model: modelValue(input.Model), Thinking: thinkingValue(input.Thinking), Worktree: input.Worktree != nil && *input.Worktree}
	if hasToolContext {
		spec.ParentContext = &toolContext.Context
		spec.ParentConfig = &toolContext.Config
	}
	runtime, err := m.factory(ctx, spec)
	if err != nil {
		m.finishLaunchFailure(stateID, token, input)
		return taskpkg.AgentLaunched{}, err
	}
	if err := m.launch(ctx, stateID, runtime, input, func() (uint64, error) {
		return m.store.AttachAgentRuntime(stateID, runtime, input.Prompt)
	}); err != nil {
		m.finishLaunchFailure(stateID, token, input)
		return taskpkg.AgentLaunched{}, err
	}
	return taskpkg.AgentLaunched{Status: "async_launched", AgentID: stateID, Description: input.Description, Prompt: input.Prompt, SubagentType: agentType, State: taskpkg.StatusRunning, OutputFile: runtime.OutputFile, WorktreePath: runtime.WorktreePath}, nil
}

// finishLaunchFailure 保留启动窗口内已被停止的任务,让父 agent 仍能收到终态通知
func (m *Manager) finishLaunchFailure(taskID string, token uint64, input taskpkg.AgentInput) {
	task, ok := m.store.Task(taskID)
	if ok && task.Base().LifecycleToken == token && task.Base().Status == taskpkg.StatusKilled {
		m.store.EnqueueNotification(taskID, token, m.notification(taskID, token, taskpkg.Notification{
			TaskID:  taskID,
			Status:  taskpkg.StatusKilled,
			Reason:  task.Base().StopReason,
			Summary: taskpkg.StoppedAgentSummary(input.Description, task.Base().StopReason),
		}))
		return
	}
	m.store.DiscardReserved(taskID)
}

// Continue 用同一个稳定 agent ID 恢复终态任务。旧 lifecycle 先取消并完全 drain,
// 然后才挂上新 runtime。
func (m *Manager) Continue(ctx context.Context, taskID, prompt string) (taskpkg.AgentLaunched, error) {
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(prompt) == "" {
		return taskpkg.AgentLaunched{}, errors.New("agent: task ID and prompt are required")
	}
	if m.factory == nil {
		return taskpkg.AgentLaunched{}, errors.New("agent: runtime factory is unavailable")
	}
	m.continuationMu.Lock()
	defer m.continuationMu.Unlock()

	task, ok := m.store.Task(taskID)
	if !ok {
		return taskpkg.AgentLaunched{}, &taskpkg.StopTaskError{Code: "not_found", Message: fmt.Sprintf("No task found with ID: %s", taskID)}
	}
	if task.Base().Status == taskpkg.StatusRunning {
		return taskpkg.AgentLaunched{}, errors.New("agent: task is still running; queue input instead")
	}
	oldRuntime, _, err := m.store.ContinuationRuntime(taskID)
	if err != nil {
		return taskpkg.AgentLaunched{}, err
	}
	if oldRuntime != nil {
		if oldRuntime.Cancel != nil {
			oldRuntime.Cancel()
		}
		if oldRuntime.Done != nil {
			select {
			case <-oldRuntime.Done:
			case <-ctx.Done():
				return taskpkg.AgentLaunched{}, ctx.Err()
			}
		}
	}

	agentTask, ok := task.(*taskpkg.AgentState)
	if !ok {
		return taskpkg.AgentLaunched{}, &taskpkg.StopTaskError{Code: "unsupported_type", Message: fmt.Sprintf("Task %s is not an agent task", taskID)}
	}
	agentType := agentTask.AgentType
	runtime, err := m.factory(ctx, taskpkg.AgentSpec{
		ID: taskID, Description: task.Base().Description, Prompt: prompt,
		AgentType:       agentType,
		WorktreePath:    agentTask.WorktreePath,
		Worktree:        agentTask.Worktree,
		PendingMessages: taskpkg.CloneUserMessages(agentTask.PendingSteering),
		Model:           modelFromState(agentTask), Thinking: agentTask.Thinking, Continuation: true,
	})
	if err != nil {
		return taskpkg.AgentLaunched{}, err
	}
	input := taskpkg.AgentInput{Description: task.Base().Description, Prompt: prompt, SubagentType: &agentType}
	if err := m.launch(ctx, taskID, runtime, input, func() (uint64, error) {
		return m.store.RegisterContinuation(taskID, runtime, prompt)
	}); err != nil {
		return taskpkg.AgentLaunched{}, err
	}
	return taskpkg.AgentLaunched{Status: "async_launched", AgentID: taskID, Description: task.Base().Description, Prompt: prompt, SubagentType: agentType, State: taskpkg.StatusRunning, OutputFile: runtime.OutputFile, WorktreePath: runtime.WorktreePath}, nil
}

// launch 校验 runtime、绑定 lifecycle context 并在后台启动任务
func (m *Manager) launch(ctx context.Context, taskID string, runtime *taskpkg.AgentRuntime, input taskpkg.AgentInput, register func() (uint64, error)) error {
	if runtime == nil || runtime.Agent == nil {
		return errors.New("agent: factory returned no agent")
	}
	if runtime.Done == nil {
		runtime.Done = make(chan struct{})
	}
	// Agent 工具是显式异步的。工具批次在返回结果后就会取消其子 context,
	// 因此要把任务 lifecycle 与该短命请求 context 解绑。TaskStop 仍是子任务的
	// 显式取消边界。
	lifecycleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	runtime.Cancel = cancel
	token, err := register()
	if err != nil {
		cancel()
		return err
	}
	m.store.SetRuntimeMetadata(taskID, token, runtime)
	m.installPendingDrain(taskID, token, runtime)
	go m.runAgent(lifecycleCtx, taskID, token, runtime, input)
	return nil
}

func modelFromState(state *taskpkg.AgentState) llm.Model {
	if state.Model == nil {
		return llm.Model{}
	}
	return *state.Model
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

func (m *Manager) runAgent(ctx context.Context, taskID string, token uint64, runtime *taskpkg.AgentRuntime, input taskpkg.AgentInput) {
	started := time.Now()
	// goroutine 边界是唯一可靠的 panic 捕获点。panic 会跳过终态转换、runtime 移除和
	// Done 关闭,任务永久卡在 running,所以在这里兜成 failed 并走完整清理路径
	defer func() {
		// 先注册资源释放,它在收尾逻辑之后执行:即使 failAgent 自己 panic,
		// cancel、runtime 移除和 Done 关闭也照常发生
		defer func() {
			if runtime.Cancel != nil {
				runtime.Cancel()
			}
			m.store.RemoveRuntime(taskID, token)
			// 先回收孤儿 bash 再关闭 Done:Continue 只等 Done,
			// 提前关闭会让新 lifecycle 与旧进程并发,清理还会误杀新任务
			m.store.KillBashTasksForAgent(taskID)
			close(runtime.Done)
		}()
		// 收尾路径自身的 panic 只记录,不能带崩进程
		defer func() {
			if nested := recover(); nested != nil {
				fmt.Fprintf(os.Stderr, "agent: panic while finishing task %s: %v\n%s\n", taskID, nested, debug.Stack())
			}
		}()
		recovered := recover()
		if recovered != nil {
			fmt.Fprintf(os.Stderr, "agent: panic in task %s: %v\n%s\n", taskID, recovered, debug.Stack())
			m.failAgent(taskID, token, runtime, input, fmt.Errorf("agent: panic: %v", recovered), started)
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
	stopReason := taskpkg.StopReasonAborted
	if task, ok := m.store.Task(taskID); ok {
		if task.Base().LifecycleToken == token && task.Base().Status == taskpkg.StatusKilled {
			wasKilled = true
			stopReason = task.Base().StopReason
		}
	}
	if wasKilled || errors.Is(ctx.Err(), context.Canceled) || errors.Is(runErr, context.Canceled) {
		partial := latestAssistant(runtime.Agent)
		if task, ok := m.store.Task(taskID); ok && task.Base().Status == taskpkg.StatusRunning {
			// 1.取消后状态没落到 killed 说明任务卡在 running,用 failed 兜住避免悬挂
			if err := taskpkg.KillAgentWithReason(taskID, m.store, taskpkg.StopReasonAborted); err != nil {
				m.store.FailAgent(taskID, token, fmt.Errorf("agent: stop after cancellation failed: %w", err))
			}
		}
		partialResult := agentResult(taskID, runtime.Agent.AgentContext.Messages, runtime.MessageStart, partial, time.Since(started))
		status := taskpkg.StatusKilled
		if task, ok := m.store.Task(taskID); ok && task.Base().Status == taskpkg.StatusFailed {
			status = taskpkg.StatusFailed
		}
		notification := m.notification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: status, Summary: fmt.Sprintf("Agent %q was aborted", input.Description), Result: joinText(partialResult.Content), TotalTokens: partialResult.TotalTokens, ToolUses: partialResult.TotalToolUseCount, DurationMs: time.Since(started).Milliseconds()})
		if status == taskpkg.StatusKilled {
			notification.Reason = stopReason
			notification.Summary = taskpkg.StoppedAgentSummary(input.Description, stopReason)
		}
		m.cleanupWorktree(taskID, token, runtime)
		m.store.EnqueueNotification(taskID, token, notification)
	} else if runErr != nil {
		m.failAgent(taskID, token, runtime, input, runErr, started)
	} else {
		result := agentResult(taskID, runtime.Agent.AgentContext.Messages, runtime.MessageStart, reply, time.Since(started))
		transitioned := m.store.CompleteAgent(taskID, token, &result)
		notification := m.notification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: input.Description, Result: joinText(result.Content), TotalTokens: result.TotalTokens, DurationMs: result.TotalDurationMs, ToolUses: result.TotalToolUseCount})
		m.cleanupWorktree(taskID, token, runtime)
		if transitioned {
			m.store.EnqueueNotification(taskID, token, notification)
		}
	}
}

// failAgent 把任务转成 failed、清理 worktree 并入队 failed 通知。
// 正常失败和 panic 恢复共用这条路径
func (m *Manager) failAgent(taskID string, token uint64, runtime *taskpkg.AgentRuntime, input taskpkg.AgentInput, cause error, started time.Time) {
	transitioned := m.store.FailAgent(taskID, token, cause)
	notification := m.notification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusFailed, Summary: cause.Error(), DurationMs: time.Since(started).Milliseconds()})
	m.cleanupWorktree(taskID, token, runtime)
	if transitioned {
		m.store.EnqueueNotification(taskID, token, notification)
	} else if task, ok := m.store.Task(taskID); ok && task.Base().LifecycleToken == token && task.Base().Status == taskpkg.StatusKilled {
		m.store.EnqueueNotification(taskID, token, m.notification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusKilled, Reason: task.Base().StopReason, Summary: taskpkg.StoppedAgentSummary(input.Description, task.Base().StopReason), DurationMs: time.Since(started).Milliseconds()}))
	}
}

func (m *Manager) cleanupWorktree(taskID string, token uint64, runtime *taskpkg.AgentRuntime) {
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

func (m *Manager) notification(taskID string, token uint64, notification taskpkg.Notification) taskpkg.Notification {
	task, ok := m.store.Task(taskID)
	if !ok || task.Base().LifecycleToken != token {
		return notification
	}
	notification.ToolUseID = task.Base().ToolUseID
	notification.OutputFile = task.Base().OutputFile
	if agentTask, ok := task.(*taskpkg.AgentState); ok {
		notification.WorktreePath = agentTask.WorktreePath
	}
	return notification
}

func (m *Manager) installPendingDrain(taskID string, token uint64, runtime *taskpkg.AgentRuntime) {
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
		// 1.steering 在工具轮次边界注入,模型下一轮就能看到
		if messages := m.store.DrainPendingMessages(taskID, token); len(messages) > 0 {
			for i, message := range messages {
				if err := runtime.Agent.SteerMessage(message); err != nil {
					m.store.RequeuePendingMessagesFront(taskID, token, messages[i:])
					return err
				}
			}
		}
		// 2.follow-up 只在当前轮彻底结束后注入,作为下一轮的用户输入
		if messages := m.store.DrainPendingFollowUpMessages(taskID, token); len(messages) > 0 {
			for i, message := range messages {
				if err := runtime.Agent.FollowUpMessage(message); err != nil {
					m.store.RequeuePendingFollowUpMessagesFront(taskID, token, messages[i:])
					return err
				}
			}
		}
		return nil
	}
}

func (m *Manager) updateProgress(taskID string, token uint64, event llm.AgentEvent) {
	m.store.UpdateAgentProgress(taskID, token, func(progress *taskpkg.AgentProgress) {
		switch event := event.(type) {
		case llm.ToolExecutionStartEvent:
			progress.ToolUseCount++
			progress.LastActivity = &taskpkg.ToolActivity{ToolName: event.ToolName, ActivityDescription: event.ToolName}
			progress.RecentActivities = append(progress.RecentActivities, *progress.LastActivity)
			if len(progress.RecentActivities) > 5 {
				progress.RecentActivities = progress.RecentActivities[len(progress.RecentActivities)-5:]
			}
		case llm.MessageEndEvent:
			if message, ok := event.Message.(llm.AssistantMessage); ok {
				// input token 是单轮累计值,只保留最新;output 是增量,求和
				if message.Usage.InputTokens > 0 {
					progress.LatestInputTokens = message.Usage.InputTokens
				}
				progress.TokenCount += message.Usage.OutputTokens
			}
		}
	})
}

// agentResult 汇总整个 transcript 的用量。
// tool call 数与 token 都要跨轮累加,只取最后一条 assistant 会严重低估多轮子任务
func agentResult(taskID string, messages []llm.Message, start int, reply *llm.AssistantMessage, duration time.Duration) taskpkg.AgentResult {
	result := taskpkg.AgentResult{AgentID: taskID, TotalDurationMs: duration.Milliseconds()}
	// fork 继承的父消息不算子任务的用量
	if start > 0 && start <= len(messages) {
		messages = messages[start:]
	}
	for _, message := range messages {
		assistant, ok := message.(llm.AssistantMessage)
		if !ok {
			continue
		}
		result.TotalToolUseCount += countToolCalls(assistant.Content)
		// output token 是每轮增量,跨轮直接求和
		result.TotalTokens += assistant.Usage.OutputTokens
	}
	if reply == nil {
		return result
	}
	// reply 已由 commitMessages 追加进 AgentContext.Messages,
	// 上面的循环已经计过它的 output,这里不再补计
	result.Usage = reply.Usage
	for _, content := range reply.Content {
		if text, ok := content.(llm.TextContent); ok {
			result.Content = append(result.Content, taskpkg.TextBlock{Text: text.Text})
		}
	}
	return result
}

func countToolCalls(content []llm.Content) int {
	count := 0
	for _, item := range content {
		if _, ok := item.(llm.ToolCall); ok {
			count++
		}
	}
	return count
}

// joinText 拼接全部文本块,与 Claude 的 extractTextContent 保持一致
func joinText(blocks []taskpkg.TextBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
