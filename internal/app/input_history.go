package app

import (
	"context"
	"sync"
	"time"

	"github.com/ywxzm03/Rivo/internal/inputhistory"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type inputKey struct {
	generation uint64
	sequence   uint64
	sessionID  string
}

type inputFailure struct {
	origin llm.UserInputOrigin
	err    error
}

type inputHistoryState struct {
	mu         sync.Mutex
	accepted   map[inputKey]struct{}
	unreported map[inputKey]inputFailure
	notify     func(llm.UserInputOrigin, error)
	closed     bool
}

// InputHistory 由进程持有；App 只借用，不随会话切换关闭。
type InputHistory struct {
	store *inputhistory.Store
	state inputHistoryState
}

func NewInputHistory(path string, notify func(llm.UserInputOrigin, error)) *InputHistory {
	return &InputHistory{
		store: inputhistory.New(path),
		state: inputHistoryState{
			accepted:   make(map[inputKey]struct{}),
			unreported: make(map[inputKey]inputFailure),
			notify:     notify,
		},
	}
}

func keyOfInput(origin llm.UserInputOrigin) inputKey {
	return inputKey{generation: origin.Generation, sequence: origin.Sequence, sessionID: origin.SessionID}
}

// Accept 只校验并入队；同步失败由调用方决定何时展示，避免 Update 自锁。
func (h *InputHistory) Accept(origin llm.UserInputOrigin) error {
	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	key := keyOfInput(origin)
	if h.state.closed {
		h.state.unreported[key] = inputFailure{origin: origin, err: inputhistory.ErrClosed}
		return inputhistory.ErrClosed
	}
	if _, exists := h.state.accepted[key]; exists {
		return nil
	}
	// 保存失败也确认身份，模型重试不会自动重写同一次输入。
	h.state.accepted[key] = struct{}{}
	err := h.store.Append(inputhistory.Record{SessionID: origin.SessionID, Timestamp: time.Now().Unix(), Text: origin.Text}, func(err error) {
		if err == nil {
			return
		}
		h.state.mu.Lock()
		h.state.unreported[key] = inputFailure{origin: origin, err: err}
		h.state.mu.Unlock()
		h.notifyFailure(origin, err)
	})
	if err != nil {
		h.state.unreported[key] = inputFailure{origin: origin, err: err}
	}
	return err
}

func (h *InputHistory) notifyFailure(origin llm.UserInputOrigin, err error) {
	h.state.mu.Lock()
	notify := h.state.notify
	h.state.mu.Unlock()
	if notify != nil {
		notify(origin, err)
	}
}

func (h *InputHistory) Acknowledge(origin llm.UserInputOrigin) {
	h.state.mu.Lock()
	delete(h.state.unreported, keyOfInput(origin))
	h.state.mu.Unlock()
}

func (h *InputHistory) Barrier() {
	h.store.Barrier()
}

func (h *InputHistory) Snapshot(ctx context.Context) (*inputhistory.View, inputhistory.ScanIssues, error) {
	return h.store.Snapshot(ctx)
}

func (h *InputHistory) Read(ctx context.Context, view *inputhistory.View, offset int) (inputhistory.Record, error) {
	return h.store.Read(ctx, view, offset)
}

// Close 排空已接纳项；错误保留到 UI 明确确认，重复关闭也能取回未展示错误。
func (h *InputHistory) Close() []error {
	h.state.mu.Lock()
	h.state.closed = true
	h.state.mu.Unlock()
	h.store.Close()
	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	var failures []error
	for _, failure := range h.state.unreported {
		failures = append(failures, failure.err)
	}
	return failures
}

func (a *App) BindInputHistory(history *InputHistory) {
	a.historyMu.Lock()
	a.inputHistory = history
	a.historyMu.Unlock()
}

func (a *App) boundInputHistory() *InputHistory {
	a.historyMu.RLock()
	history := a.inputHistory
	a.historyMu.RUnlock()
	return history
}

func (a *App) inputAccepted(origin llm.UserInputOrigin) {
	if history := a.boundInputHistory(); history != nil {
		if err := history.Accept(origin); err != nil {
			history.notifyFailure(origin, err)
		}
	}
}
