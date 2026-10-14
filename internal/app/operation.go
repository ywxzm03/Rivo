package app

import (
	"context"
	"errors"
	"strings"

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
)

type OperationRequest struct {
	Kind  OperationKind
	Value string
}

type OperationResult struct {
	Replayed bool
	Reply    *llm.AssistantMessage
	Next     *App
	Err      error
}

// Operation 的完成信号晚于资源收尾和 App 所有权释放；可反复等待同一结果
type Operation struct {
	ID     uint64
	cancel context.CancelFunc
	done   chan struct{}
	result OperationResult
}

func (o *Operation) Cancel()               { o.cancel() }
func (o *Operation) Wait() OperationResult { <-o.done; return o.result }

// Start 接受后立即执行操作，一个 App 同时只接受一个会话修改，不隐式排队
func (a *App) Start(ctx context.Context, request OperationRequest) (*Operation, error) {
	a.operationMu.Lock()
	if a.active != nil {
		a.operationMu.Unlock()
		return nil, errors.New("app: an operation is running")
	}
	ctx, cancel := context.WithCancel(ctx)
	a.operationID++
	op := &Operation{ID: a.operationID, cancel: cancel, done: make(chan struct{})}
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
		if errors.As(result.Err, &commitErr) && a.pendingRequest == nil {
			copy := request
			a.pendingRequest = &copy
		}
	}()
	// 1.补写原候选记录，不重跑模型或工具；同一操作重试从已提交边界续接
	if a.session.HasPending() {
		messages := a.session.PendingMessages()
		pendingEdit := a.session.PendingContextEdit()
		previous := a.pendingRequest
		if result.Err = a.session.RetryPending(); result.Err != nil {
			return result
		}
		a.pendingRequest = nil
		if result.Err = a.restoreCommittedSettings(); result.Err != nil {
			return result
		}
		result.Replayed = true
		if pendingEdit != nil && pendingEdit.Replacement == nil && a.coordinator.pendingRecovery {
			resume := previous != nil && *previous == request && request.Kind == OperationRun
			result.Reply, result.Err = a.coordinator.retryOverflowRecovery(ctx, resume)
			if result.Err != nil || resume {
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
				result.Reply, result.Err = a.agent.Continue(ctx)
				result.Reply, result.Err = a.coordinator.finishRun(ctx, result.Reply, result.Err)
				return result
			}
			if len(messages) > 0 {
				switch last := messages[len(messages)-1].(type) {
				case llm.AssistantMessage:
					result.Reply, result.Err = a.agent.Resume(ctx, &last)
					result.Reply, result.Err = a.coordinator.finishRun(ctx, result.Reply, result.Err)
					return result
				case llm.ToolResultMessage, llm.UserMessage:
					result.Reply, result.Err = a.agent.Continue(ctx)
					result.Reply, result.Err = a.coordinator.finishRun(ctx, result.Reply, result.Err)
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
	case OperationCompact:
		result.Err = a.coordinator.Compact(ctx, request.Value)
	case OperationModel:
		provider, model, _ := strings.Cut(request.Value, "/")
		result.Err = a.setModel(provider, model)
	case OperationThinking:
		result.Err = a.setThinkingLevel(llm.ThinkingLevel(request.Value))
	case OperationResume:
		result.Next, result.Err = Open(request.Value, a.cfg, a.onEvent)
	default:
		result.Err = errors.New("app: unknown operation")
	}
	return result
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
