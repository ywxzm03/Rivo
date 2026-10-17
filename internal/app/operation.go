package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	RestoreMode filehistory.RestoreMode
}

type OperationResult struct {
	Replayed bool
	Reply    *llm.AssistantMessage
	Next     *App
	Exit     bool
	// EditorText is set after a successful tree rewind so the TUI can restore
	// the selected user message without persisting a branch marker.
	EditorText     string
	RestoreEditor  bool
	ContextRewound bool
	FileRestore    *filehistory.RestoreResult
	Err            error
}

// Operation 的完成信号晚于资源收尾和 App 所有权释放；可反复等待同一结果
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

// EnqueueSteering 接受运行中的一条 steering；压缩或操作收尾期间拒绝输入
func (a *App) EnqueueSteering(input string) error {
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if a.active == nil || a.active.kind != OperationRun || a.active.closing {
		return ErrSteeringUnavailable
	}
	return a.coordinator.EnqueueSteering(input)
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

// Cancel 只发送信号；退出路径随后调用 Wait 等待流、工具与持久化收尾
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
	// 1.补写原候选记录，不重跑模型或工具；同一操作重试从已提交边界续接
	if a.session.HasPending() && request.Kind != OperationDelete {
		messages := a.session.PendingMessages()
		pendingEdit := a.session.PendingContextEdit()
		previous := a.pendingRequest
		if result.Err = a.session.RetryPending(); result.Err != nil {
			return result
		}
		a.pendingRequest = nil
		a.acknowledgePendingSteering(messages)
		if result.Err = a.restoreCommittedSettings(); result.Err != nil {
			return result
		}
		result.Replayed = true
		if pendingEdit != nil && pendingEdit.Replacement == nil && a.coordinator.pendingRecovery {
			resume := previous != nil && *previous == request && request.Kind == OperationRun
			result.Reply, result.Err = a.coordinator.retryOverflowRecovery(ctx, resume)
			if result.Err != nil || resume {
				if resume {
					result = a.continueQueuedSteering(ctx, result)
				}
				return result
			}
		}
		a.coordinator.resetRecovery()
		if previous != nil && *previous == request {
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
	// 2.执行本次操作，运行中的核心只通过请求快照与提交边界读取宿主状态
	switch request.Kind {
	case OperationRun:
		result.Reply, result.Err = a.coordinator.Run(ctx, request.Value)
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

// continueQueuedSteering 在释放操作所有权前消费已接受的 steering，并关闭收尾窗口
func (a *App) continueQueuedSteering(ctx context.Context, result OperationResult) OperationResult {
	for result.Err == nil && ctx.Err() == nil {
		a.operationMu.Lock()
		continueRun := a.active != nil && !a.active.closing && a.agent.HasSteering()
		if !continueRun && a.active != nil {
			a.active.closing = true
		}
		a.operationMu.Unlock()
		if !continueRun {
			break
		}
		reply, err := a.coordinator.ContinueQueued(ctx)
		if reply != nil {
			result.Reply = reply
		}
		result.Err = err
	}
	return result
}

func (a *App) acknowledgePendingSteering(messages []llm.Message) {
	for _, message := range messages {
		user, ok := message.(llm.UserMessage)
		if !ok || len(user.Content) != 1 {
			continue
		}
		text, ok := user.Content[0].(llm.TextContent)
		if ok {
			a.agent.AcknowledgeSteering(text.Text)
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

// rewindTree moves the in-memory leaf to the selected user parent and restores
// the model settings projected by that branch. Any setup failure rolls the leaf
// back so a failed UI operation cannot leave the app on a partial branch.
func (a *App) rewindTree(entryID string) (string, bool, error) {
	previousLeaf := a.session.LeafID()
	rewound, err := a.session.RewindUser(entryID)
	if err != nil {
		return "", false, err
	}
	if err := a.restoreBranchSettings(); err != nil {
		if rollbackErr := a.restoreLeaf(previousLeaf, entryID); rollbackErr != nil {
			return "", false, errors.Join(err, rollbackErr)
		}
		return "", false, err
	}
	return rewound.Text, true, nil
}

// rewindTreeWithMode 把代码恢复放在上下文回滚之前，保证组合操作失败时不移动上下文。
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
	text, restoreEditor, err := a.rewindTree(entryID)
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

// restoreBranchSettings applies settings found on the newly active path and
// falls back to the app defaults when the branch has no explicit setting.
func (a *App) restoreBranchSettings() error {
	view := a.session.BuildContext()
	provider, model := view.Provider, view.Model
	if provider == "" || model == "" {
		provider, model = a.cfg.ProviderID, a.cfg.ModelID
	}
	if provider != "" {
		if model == "" {
			spec, err := a.cfg.lookupProvider(provider)
			if err != nil {
				return err
			}
			model = spec.defaultModel
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
	a.agent.AgentConfig.ThinkingLevel = level
	return nil
}

// restoreCommittedSettings 发布补写成功的设置，不追加第二条设置记录
func (a *App) restoreCommittedSettings() error {
	view := a.session.BuildContext()
	if view.Provider != "" && view.Model != "" {
		if err := a.applyModel(view.Provider, view.Model, false); err != nil {
			return err
		}
	}
	if view.HasThinkingLevel {
		a.agent.AgentConfig.ThinkingLevel = llm.ThinkingLevel(view.ThinkingLevel)
	}
	a.agent.ReplaceMessages(view.Messages())
	return nil
}
