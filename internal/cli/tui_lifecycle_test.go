package cli

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestTUISendDoesNotBlockCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sending, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := &tuiRuntime{cancel: cancel, send: func(tea.Msg) { close(sending); <-release }}
	go func() {
		defer close(finished)
		_ = r.handleEvent(llm.CompactionStartEvent{})
	}()
	<-sending
	go r.cancelCurrent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		close(release)
		<-finished
		t.Fatal("UI notification held the cancellation lock")
	}
	close(release)
	<-finished
}

func TestTUIRequestStartsBeforeCommandAndSettlesBeforeCompletion(t *testing.T) {
	useTestEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &tuiRuntime{app: a, cfg: cfg}
	cmd := r.request(tui.Request{Kind: "model", Value: "unknown/unknown"})
	settled := make(chan struct{})
	go func() { r.waitCurrent(); close(settled) }()
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("accepted request depends on tea.Cmd scheduling")
	}
	if result := cmd().(tui.OperationDoneMsg); result.Err == nil {
		t.Fatal("invalid model should report its error")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil || r.done != nil {
		t.Fatal("completion published before runtime ownership was released")
	}
}

func TestTUIShutdownWaitsForCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r := &tuiRuntime{cancel: cancel, done: done}
	joined := make(chan struct{})
	go func() { r.cancelCurrent(); r.waitCurrent(); close(joined) }()
	<-ctx.Done()
	select {
	case <-joined:
		t.Fatal("shutdown returned before cleanup finished")
	default:
	}
	close(done)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join the settled operation")
	}
}
