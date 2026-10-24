package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestStatusReportsModelUsageAndSessionIdentity(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a, err := app.NewWithSessionDir(cfg, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{app: a, cfg: cfg}
	msg, ok := r.Status()().(tui.SessionStatusMsg)
	if !ok || msg.Err != nil {
		t.Fatalf("Status() = %+v", msg)
	}
	model := a.CurrentModel()
	if msg.Model != model.ID || msg.Provider != model.Provider {
		t.Errorf("model = %s/%s, want %s/%s", msg.Provider, msg.Model, model.Provider, model.ID)
	}
	if msg.SessionID != a.SessionID() || msg.Transcript != a.SessionPath() {
		t.Errorf("session = %q / %q, want %q / %q", msg.SessionID, msg.Transcript, a.SessionID(), a.SessionPath())
	}
	if !filepath.IsAbs(msg.Transcript) || filepath.Dir(msg.Transcript) != dir {
		t.Errorf("transcript = %q, want an absolute path inside %q", msg.Transcript, dir)
	}
	if !msg.Available || msg.ContextWindow != model.ContextWindow || msg.Tokens != 0 {
		t.Errorf("usage = %+v, want 0/%d available", msg, model.ContextWindow)
	}
	if msg.ThreadName != "" {
		t.Errorf("thread name = %q, want empty for an unnamed session", msg.ThreadName)
	}
}

func TestStatusWithoutAppReportsError(t *testing.T) {
	if msg, ok := (&Runtime{}).Status()().(tui.SessionStatusMsg); !ok || msg.Err == nil {
		t.Fatalf("Status() = %+v, want error", msg)
	}
}

func TestStatusTranscriptUsesPendingPathBeforeFirstMessage(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := a.SessionPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session file exists before the first message: %v", err)
	}
	msg := (&Runtime{app: a, cfg: cfg}).Status()().(tui.SessionStatusMsg)
	if msg.Transcript != path {
		t.Errorf("transcript = %q, want the pending path %q", msg.Transcript, path)
	}
}
