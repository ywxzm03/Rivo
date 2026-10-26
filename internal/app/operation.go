package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

type OperationKind string

const (
	OperationRun      OperationKind = "run"
	OperationCompact  OperationKind = "compact"
	OperationModel    OperationKind = "model"
	OperationThinking OperationKind = "thinking"
	OperationResume   OperationKind = "resume"
	OperationRename   OperationKind = "rename"
	OperationNew      OperationKind = "new"
	OperationReload   OperationKind = "reload"
	OperationTree     OperationKind = "tree"
	OperationClear    OperationKind = "clear"
	OperationDelete   OperationKind = "delete"
)

type OperationRequest struct {
	Kind        OperationKind
	Value       string
	Message     *llm.UserMessage
	RestoreMode filehistory.RestoreMode
	// NotificationOnly 表示这轮 run 只为投递子任务通知,不追加用户输入
	NotificationOnly bool
}

type OperationResult struct {
	Replayed bool
	Reply    *llm.AssistantMessage
	Next     *App
	Exit     bool
	// EditorText 在 tree 回退成功后由 TUI 填回编辑器
	EditorText     string
	RestoreEditor  bool
	EditorMessage  *llm.UserMessage
	ContextRewound bool
	FileRestore    *filehistory.RestoreResult
	Err            error
}

// Operation 的完成信号晚于资源收尾和 App 所有权释放，可反复等待同一结果
type Operation struct {
	ID      uint64
	kind    OperationKind
	closing bool
	cancel  context.CancelFunc
	done    chan struct{}
	result  OperationResult
}

func (o *Operation) Cancel()               { o.cancel() }
func (o *Operation) Wait() OperationResult { <-o.done; return o.result }

var ErrSteeringUnavailable = errors.New("app: steering is unavailable")

func (a *App) EnqueueSteering(input string) error {
	return a.EnqueueSteeringMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

func (a *App) EnqueueSteeringMessage(input llm.UserMessage) error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if a.active == nil || a.active.kind != OperationRun || a.active.closing {
		return ErrSteeringUnavailable
	}
	return a.coordinator.EnqueueSteeringMessage(input)
}

var ErrFollowUpUnavailable = errors.New("app: follow-up is unavailable")

func (a *App) EnqueueFollowUp(input string) error {
	return a.EnqueueFollowUpMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: input}}})
}

func (a *App) EnqueueFollowUpMessage(input llm.UserMessage) error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if a.active == nil || a.active.kind != OperationRun || a.active.closing {
		return ErrFollowUpUnavailable
	}
	return a.coordinator.EnqueueFollowUpMessage(input)
}

// Start 接受后立即执行操作，一个 App 同时只接受一个会话修改，不隐式排队
func (a *App) Start(ctx context.Context, request OperationRequest) (*Operation, error) {
	a.operationMu.Lock()
	if a.active != nil {
		a.operationMu.Unlock()
		return nil, errors.New("app: an operation is running")
	}
	ctx, cancel := context.WithCancel(ctx)
	a.operationID++
	op := &Operation{ID: a.operationID, kind: request.Kind, cancel: cancel, done: make(chan struct{})}
	a.active = op
	a.operationMu.Unlock()
	go func() {
		op.result = a.executeOperation(ctx, request)
		cancel()
		a.operationMu.Lock()
		a.active = nil
		close(op.done)
		a.operationMu.Unlock()
	}()
	return op, nil
}

// Cancel 只发送信号，调用方需随后 Wait 等待收尾
func (a *App) Cancel() {
	a.operationMu.Lock()
	op := a.active
	if op != nil {
		op.closing = true
	}
	a.operationMu.Unlock()
	if op != nil {
		op.Cancel()
	}
}

func (a *App) Wait() {
	a.operationMu.Lock()
	op := a.active
	a.operationMu.Unlock()
	if op != nil {
		op.Wait()
	}
}

func (a *App) executeOperation(ctx context.Context, request OperationRequest) (result OperationResult) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	defer func() {
		a.agent.ReplaceMessages(a.session.BuildContext().Messages())
		var commitErr *session.CommitError
		if errors.As(result.Err, &commitErr) && a.session.HasPending() && a.pendingRequest == nil {
			copy := request
			a.pendingRequest = &copy
		}
		a.operationMu.Lock()
		if a.active != nil {
			a.active.closing = true
		}
		a.operationMu.Unlock()
	}()
	if request.Kind == OperationRun && !a.CurrentModel().SupportsImages {
		if request.Message != nil && userMessageHasImages(*request.Message) {
			result.Err = errors.New("app: current model does not support image input")
			return result
		}
		if a.session.HasPending() && pendingMessagesHaveImages(a.session.PendingMessages()) {
			result.Err = errors.New("app: pending image input requires a vision model")
			return result
		}
	}
	// 1.补写原候选记录，不重跑模型或工具；同一操作重试从已提交边界续接
	if a.session.HasPending() && request.Kind != OperationDelete {
		messages := a.session.PendingMessages()
		pendingEdit := a.session.PendingContextEdit()
		previous := a.pendingRequest
		if result.Err = a.session.RetryPending(); result.Err != nil {
			return result
		}
		a.pendingRequest = nil
		a.acknowledgePendingQueued(messages)
		if result.Err = a.restoreCommittedSettings(); result.Err != nil {
			return result
		}
		result.Replayed = true
		if pendingEdit != nil && pendingEdit.Replacement == nil && a.coordinator.pendingRecovery {
			resume := previous != nil && equalOperationRequest(*previous, request) && request.Kind == OperationRun
			result.Reply, result.Err = a.coordinator.retryOverflowRecovery(ctx, resume)
			if result.Err != nil || resume {
				if resume {
					result = a.continueQueuedSteering(ctx, result)
				}
				return result
			}
		}
		a.coordinator.resetRecovery()
		if previous != nil && equalOperationRequest(*previous, request) {
			if request.Kind != OperationRun {
				return result
			}
			if len(messages) == 0 {
				// 压缩点或省略标记已补写，续接原请求，不重复记录用户输入
				result.Reply, result.Err = a.coordinator.Continue(ctx)
				result = a.continueQueuedSteering(ctx, result)
				return result
			}
			if len(messages) > 0 {
				switch last := messages[len(messages)-1].(type) {
				case llm.AssistantMessage:
					result.Reply, result.Err = a.agent.Resume(ctx, &last)
					result.Reply, result.Err = a.coordinator.finishRun(ctx, result.Reply, result.Err)
					result = a.continueQueuedSteering(ctx, result)
					return result
				case llm.ToolResultMessage, llm.UserMessage:
					result.Reply, result.Err = a.coordinator.Continue(ctx)
					result = a.continueQueuedSteering(ctx, result)
					return result
				}
			}
		}
		// 用户开启不同操作时，未启动的工具以取消结果封闭，不制造新的工具副作用
		if len(messages) == 1 {
			if assistant, ok := messages[0].(llm.AssistantMessage); ok {
				var canceled []llm.Message
				for _, content := range assistant.Content {
					if call, ok := content.(llm.ToolCall); ok {
						canceled = append(canceled, llm.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, IsError: true, Content: []llm.Content{llm.TextContent{Text: "Operation aborted"}}})
					}
				}
				if len(canceled) > 0 {
					_, result.Err = a.session.AppendMessages(canceled)
					if result.Err != nil {
						return result
					}
				}
			}
		}
	}
	// 2.执行本次操作
	switch request.Kind {
	case OperationRun:
		if request.Message != nil && userMessageHasImages(*request.Message) && !a.CurrentModel().SupportsImages {
			result.Err = errors.New("app: current model does not support image input")
			return result
		}
		switch {
		case request.NotificationOnly:
			// 通知已作为 transient 附件挂到请求上,空 Continue 只跑这一轮
			result.Reply, result.Err = a.coordinator.Continue(ctx)
		case request.Message != nil:
			result.Reply, result.Err = a.coordinator.RunMessage(ctx, *request.Message)
		default:
			result.Reply, result.Err = a.coordinator.Run(ctx, request.Value)
		}
		result = a.continueQueuedSteering(ctx, result)
	case OperationCompact:
		result.Err = a.coordinator.Compact(ctx, request.Value)
	case OperationModel:
		provider, model, _ := strings.Cut(request.Value, "/")
		result.Err = a.setModel(provider, model)
	case OperationThinking:
		result.Err = a.setThinkingLevel(llm.ThinkingLevel(request.Value))
	case OperationResume:
		result.Next, result.Err = Open(request.Value, a.cfg, a.onEvent)
	case OperationRename:
		result.Err = a.session.AppendSessionInfo(request.Value)
	case OperationNew:
		result.Next, result.Err = a.newSession()
	case OperationReload:
		a.Reload()
	case OperationTree:
		treeResult, err := a.rewindTreeWithMode(request.Value, request.RestoreMode)
		result = treeResult
		result.Err = err
	case OperationClear:
		result.Err = a.clearSession()
	case OperationDelete:
		result.Err = a.session.Delete()
		result.Exit = result.Err == nil
	default:
		result.Err = errors.New("app: unknown operation")
	}
	return result
}

func userMessageHasImages(message llm.UserMessage) bool {
	for _, content := range message.Content {
		if _, ok := content.(llm.ImageContent); ok {
			return true
		}
	}
	return false
}

func pendingMessagesHaveImages(messages []llm.Message) bool {
	for _, message := range messages {
		if user, ok := message.(llm.UserMessage); ok && userMessageHasImages(user) {
			return true
		}
	}
	return false
}

func equalOperationRequest(left, right OperationRequest) bool {
	return reflect.DeepEqual(left, right)
}

// continueQueuedSteering 在释放操作所有权前完成收尾：steering 优先，follow-up 后继，
// 最后才是子任务通知；每完成一次后继 run 都重新检查三类来源，
// 避免收尾窗口内新入队的消息或新到达的通知被遗留
func (a *App) continueQueuedSteering(ctx context.Context, result OperationResult) OperationResult {
	for result.Err == nil && ctx.Err() == nil {
		a.operationMu.Lock()
		open := a.active != nil && !a.active.closing
		steering := open && a.agent.Len(agent.QueueSteering) > 0
		followUp := open && !steering && a.agent.Len(agent.QueueFollowUp) > 0
		notified := open && !steering && !followUp && a.hasPendingNotification()
		if !steering && !followUp && !notified {
			// 最终队列检查与关闭收尾窗口必须同处一个临界区，
			// 否则检查通过后的新入队会留在队列里无人消费
			if a.active != nil {
				a.active.closing = true
			}
			a.operationMu.Unlock()
			return result
		}
		a.operationMu.Unlock()
		var reply *llm.AssistantMessage
		var err error
		switch {
		case steering:
			reply, err = a.coordinator.ContinueQueued(ctx)
		case followUp:
			reply, err = a.coordinator.ContinueFollowUp(ctx)
		default:
			// 通知是 transient 请求附件,空 Continue 只带着它跑一轮
			reply, err = a.coordinator.Continue(ctx)
		}
		if reply != nil {
			result.Reply = reply
		}
		result.Err = err
	}
	a.operationMu.Lock()
	if a.active != nil {
		a.active.closing = true
	}
	a.operationMu.Unlock()
	return result
}

// hasPendingNotification 报告根 store 是否还有待投递的子任务终态通知
func (a *App) hasPendingNotification() bool {
	store := a.SubagentStore()
	return store != nil && len(store.PeekNotifications()) > 0
}

// acknowledgePendingQueued 只确认最近一次 commit 失败恢复的那条队列项；
// 不按文本跨队列删除，避免同文本 steering/follow-up 互相误删
func (a *App) acknowledgePendingQueued(messages []llm.Message) {
	for _, message := range messages {
		user, ok := message.(llm.UserMessage)
		if !ok {
			continue
		}
		if a.agent.AcknowledgePending(agent.PayloadFromMessage(user)) {
			continue
		}
		if len(user.Content) == 1 {
			if text, ok := user.Content[0].(llm.TextContent); ok {
				a.agent.AcknowledgePendingText(text.Text)
			}
		}
	}
}

// clearSession 移动当前叶子到空根，保留会话文件与运行时设置
func (a *App) clearSession() error {
	if err := a.session.ResetLeaf(); err != nil {
		return err
	}
	a.coordinator.resetRecovery()
	return nil
}

// rewindTree 把内存 leaf 移到目标 user 的父节点并恢复该分支的模型设置，
// 失败时回滚 leaf，避免留下部分生效的分支
func (a *App) rewindTree(entryID string) (string, *llm.UserMessage, bool, error) {
	previousLeaf := a.session.LeafID()
	rewound, err := a.session.RewindUser(entryID)
	if err != nil {
		return "", nil, false, err
	}
	if err := a.restoreBranchSettings(); err != nil {
		if rollbackErr := a.restoreLeaf(previousLeaf, entryID); rollbackErr != nil {
			return "", nil, false, errors.Join(err, rollbackErr)
		}
		return "", nil, false, err
	}
	return rewound.Text, rewound.Message, true, nil
}

// rewindTreeWithMode 把代码恢复放在上下文回滚之前，保证组合操作失败时不移动上下文
func (a *App) rewindTreeWithMode(entryID string, mode filehistory.RestoreMode) (OperationResult, error) {
	if mode == "" {
		mode = filehistory.RestoreContext
	}
	if mode != filehistory.RestoreContext && mode != filehistory.RestoreCode && mode != filehistory.RestoreCodeAndContext {
		return OperationResult{}, fmt.Errorf("app: unknown tree restore mode %q", mode)
	}
	result := OperationResult{}
	var rollbackCode func() error
	if mode == filehistory.RestoreCode || mode == filehistory.RestoreCodeAndContext {
		if a.session.FileHistory() == nil {
			return result, errors.New("app: file history is unavailable")
		}
		var restored filehistory.RestoreResult
		var err error
		if mode == filehistory.RestoreCodeAndContext {
			restored, rollbackCode, err = a.session.FileHistory().RestoreWithRollback(entryID)
		} else {
			restored, err = a.session.FileHistory().Restore(entryID)
		}
		result.FileRestore = &restored
		if err != nil {
			return result, err
		}
	}
	if mode == filehistory.RestoreCode {
		return result, nil
	}
	text, editorMessage, restoreEditor, err := a.rewindTree(entryID)
	if err != nil {
		if rollbackCode != nil {
			if rollbackErr := rollbackCode(); rollbackErr != nil {
				return result, errors.Join(err, fmt.Errorf("app: code restore compensation failed: %w", rollbackErr))
			}
			result.FileRestore = nil
			return result, fmt.Errorf("app: context restore failed; code restore was rolled back: %w", err)
		}
		return result, err
	}
	a.session.PruneFileHistoryToActiveBranch(entryID)
	result.EditorText = text
	result.EditorMessage = editorMessage
	result.RestoreEditor = restoreEditor
	result.ContextRewound = true
	return result, nil
}

func (a *App) restoreLeaf(leafID string, keepIDs ...string) error {
	if leafID == "" {
		return a.session.ResetLeaf(keepIDs...)
	}
	return a.session.Branch(leafID, keepIDs...)
}

// restoreBranchSettings 应用新活跃路径上的设置，分支无显式设置时回退到 App 默认值
func (a *App) restoreBranchSettings() error {
	view := a.session.BuildContext()
	provider, model := view.Provider, view.Model
	if provider == "" || model == "" {
		provider, model = a.cfg.ProviderID, a.cfg.ModelID
	}
	if provider != "" {
		if model == "" {
			model = a.cfg.DefaultModelID(provider)
			if model == "" {
				return fmt.Errorf("unknown provider %q", provider)
			}
		}
		if err := a.applyModel(provider, model, false); err != nil {
			return err
		}
	}
	level := a.cfg.ThinkingLevel
	if view.HasThinkingLevel {
		level = llm.ThinkingLevel(view.ThinkingLevel)
	}
	if level == "" {
		level = llm.ThinkingLevelOff
	}
	a.agent.SetThinkingLevel(level)
	return nil
}

func (a *App) restoreCommittedSettings() error {
	view := a.session.BuildContext()
	if view.Provider != "" && view.Model != "" {
		if err := a.applyModel(view.Provider, view.Model, false); err != nil {
			return err
		}
	}
	if view.HasThinkingLevel {
		a.agent.SetThinkingLevel(llm.ThinkingLevel(view.ThinkingLevel))
	}
	a.agent.ReplaceMessages(view.Messages())
	return nil
}
