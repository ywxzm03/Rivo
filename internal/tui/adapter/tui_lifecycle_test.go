package adapter

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestTUISendDoesNotBlockCancellation(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	sending, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := &Runtime{send: func(tea.Msg) { close(sending); <-release }}
	a, err := app.NewWithSessionDir(cfg, t.TempDir(), r.handleEvent)
	if err != nil {
		t.Fatal(err)
	}
	r.app = a
	op, err := a.Start(context.Background(), app.OperationRequest{Kind: app.OperationRun, Value: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	<-sending
	go func() { r.cancelCurrent(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		close(release)
		op.Wait()
		t.Fatal("UI notification held cancellation lock")
	}
	r.mu.Lock()
	r.send = nil
	r.mu.Unlock()
	close(release)
	op.Wait()
}

func TestTUIRequestStartsBeforeCommandAndSettlesBeforeCompletion(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{app: a, cfg: cfg}
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
	if _, err := a.Start(context.Background(), app.OperationRequest{Kind: app.OperationThinking, Value: "off"}); err != nil {
		t.Fatal("completion retained operation ownership", err)
	}
	a.Wait()
}

func setupAdapterEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("RIVO_AGENT_DIR", filepath.Join(root, "agent"))
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
}
