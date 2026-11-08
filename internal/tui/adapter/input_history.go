package adapter

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/inputhistory"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tui"
)

type inputHistoryBinding struct {
	service      *app.InputHistory // 借用进程服务，切换 App 不关闭
	generation   uint64
	view         *inputhistory.View // 所有 scope 共用的固定文件前缀
	metadata     tui.InputHistoryMetadata
	seededScopes map[string]struct{}
	initial      *tui.InputHistoryState
}

var inputHistoryGeneration atomic.Uint64

// BindInputHistory 绑定进程服务，磁盘快照在 Attach 或成功切换会话时建立
func (r *Runtime) BindInputHistory(service *app.InputHistory) {
	r.mu.Lock()
	r.history.service = service
	a := r.app
	r.mu.Unlock()
	if a != nil {
		a.BindInputHistory(service)
	}
}

// NotifyHistoryFailure 在后台线程通知 UI，错误在展示确认前仍由服务保留
func (r *Runtime) NotifyHistoryFailure(origin llm.UserInputOrigin, err error) {
	r.mu.Lock()
	send := r.send
	r.mu.Unlock()
	if send != nil {
		send(tui.HistoryRecordMsg{Origin: uiInputOrigin(origin), Err: err})
	}
}

func domainInputOrigin(origin tui.InputOrigin) llm.UserInputOrigin {
	return llm.UserInputOrigin{Generation: origin.Generation, Sequence: origin.Sequence, SessionID: origin.SessionID, Text: origin.Text}
}

func uiInputOrigin(origin llm.UserInputOrigin) tui.InputOrigin {
	return tui.InputOrigin{Generation: origin.Generation, Sequence: origin.Sequence, SessionID: origin.SessionID, Text: origin.Text}
}

// rebuildInputHistory 的屏障、扫描与种子投影都在 Runtime 主锁之外执行
func (r *Runtime) rebuildInputHistory(a *app.App) *tui.InputHistoryState {
	r.mu.Lock()
	service := r.history.service
	r.mu.Unlock()
	if service == nil {
		return nil
	}
	a.BindInputHistory(service)
	service.Barrier()
	view, issues, err := service.Snapshot(context.Background())
	state := &tui.InputHistoryState{Metadata: tui.InputHistoryMetadata{Generation: inputHistoryGeneration.Add(1), SessionID: a.SessionID()}, ScopeID: a.SessionID(), Replay: replayInputHistory(a.ContextView().Messages()), ResetAll: true}
	if err != nil {
		state.Warnings = append(state.Warnings, "global history unavailable: "+err.Error())
	} else {
		state.Metadata.PersistentCount = view.Count()
		if issues.MalformedRows != 0 || issues.EmptyRows != 0 || issues.UnterminatedTailBytes != 0 {
			state.Warnings = append(state.Warnings, fmt.Sprintf("skipped %d malformed rows, %d empty rows; %d uncommitted tail bytes", issues.MalformedRows, issues.EmptyRows, issues.UnterminatedTailBytes))
		}
	}
	r.mu.Lock()
	if r.app == a {
		r.history = inputHistoryBinding{service: service, generation: state.Metadata.Generation, view: view, metadata: state.Metadata, seededScopes: map[string]struct{}{a.SessionID(): {}}, initial: state}
	}
	r.mu.Unlock()
	return state
}

func replayInputHistory(messages []llm.Message) []tui.ComposerHistoryEntry {
	var entries []tui.ComposerHistoryEntry
	for _, message := range messages {
		user, ok := message.(llm.UserMessage)
		if !ok || user.Hidden {
			continue
		}
		payload := payloadFromMessage(&user)
		if payload == nil {
			continue
		}
		if skill := skillInvocation(payload.Text); skill != nil {
			payload.Text = "/skill:" + skill.Name
			if skill.Arguments != "" {
				payload.Text += " " + skill.Arguments
			}
		}
		entries = append(entries, tui.ComposerHistoryEntry{Draft: tui.ComposerDraftFromPayload(*payload), Replayed: true})
	}
	return entries
}

// HistoryHandlers 构造异步读取入口，RecordCleared 同步仅入队
func (r *Runtime) HistoryHandlers() tui.HistoryHandlers {
	return tui.HistoryHandlers{
		Lookup:       r.lookupInputHistory,
		PrepareScope: r.prepareInputHistoryScope,
		RecordCleared: func(origin tui.InputOrigin) tea.Cmd {
			r.mu.Lock()
			service := r.history.service
			r.mu.Unlock()
			if service == nil {
				return nil
			}
			if err := service.Accept(domainInputOrigin(origin)); err != nil {
				return func() tea.Msg { return tui.HistoryRecordMsg{Origin: origin, Err: err} }
			}
			return nil
		},
		Acknowledge: func(origin tui.InputOrigin) {
			r.mu.Lock()
			service := r.history.service
			r.mu.Unlock()
			if service != nil {
				service.Acknowledge(domainInputOrigin(origin))
			}
		},
	}
}

func (r *Runtime) lookupInputHistory(request tui.HistoryLookupRequest) tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		service, view, generation := r.history.service, r.history.view, r.history.generation
		r.mu.Unlock()
		if service == nil || request.Generation != generation {
			return tui.HistoryLookupMsg{Request: request, Err: errors.New("input history generation changed")}
		}
		record, err := service.Read(context.Background(), view, request.Offset)
		return tui.HistoryLookupMsg{Request: request, Text: record.Text, Err: err, Unavailable: errors.Is(err, inputhistory.ErrViewChanged)}
	}
}

func (r *Runtime) prepareInputHistoryScope(request tui.ScopePrepareRequest) tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		a, generation := r.app, r.history.generation
		_, seeded := r.history.seededScopes[request.ScopeID]
		r.mu.Unlock()
		result := tui.ScopePreparedMsg{Request: request}
		if a == nil || request.Generation != generation {
			result.Err = errors.New("input scope generation changed")
			return result
		}
		if seeded {
			return result
		}
		if request.ScopeID == a.SessionID() {
			result.Replay = replayInputHistory(a.ContextView().Messages())
		} else {
			store := a.SubagentStore()
			if store == nil {
				result.Err = app.ErrSteeringUnavailable
				return result
			}
			messages, _, err := store.ReadTaskMessages(request.ScopeID, 0)
			if err != nil {
				result.Err = err
				return result
			}
			result.Replay = replayInputHistory(messages)
		}
		return result
	}
}

// inputScopeState 发布已确认的输入目标，重复快照不重新读取种子
func (r *Runtime) inputScopeState(taskID string) *tui.InputHistoryState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.history.service == nil {
		return nil
	}
	if r.app == nil || r.history.metadata.SessionID != r.app.SessionID() {
		return nil
	}
	if taskID == "" {
		taskID = r.history.metadata.SessionID
	}
	r.history.seededScopes[taskID] = struct{}{}
	if taskID == r.history.metadata.SessionID {
		return r.history.initial
	}
	return &tui.InputHistoryState{Metadata: r.history.metadata, ScopeID: taskID}
}

// Close 收尾当前 App，进程持有者随后排空并关闭历史服务
func (r *Runtime) Close() {
	r.mu.Lock()
	a := r.app
	r.mu.Unlock()
	if a != nil {
		a.Close()
	}
}
