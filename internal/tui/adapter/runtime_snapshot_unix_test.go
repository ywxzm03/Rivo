//go:build unix

package adapter

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// 会话扫描必须在 Runtime 主锁之外执行，不能拖住取消与事件处理
func TestSessionScanDoesNotBlockRuntime(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	dir, err := session.DefaultDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "slow.jsonl")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, cwd, "main")
	r.Attach(a)
	finished := make(chan tui.SessionsMsg, 1)
	go func() { finished <- r.Sessions()().(tui.SessionsMsg) }()
	// 写入端可能阻塞在 FIFO 打开上，放到独立 goroutine，避免测试自身挂死
	go func() {
		writer, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer writer.Close()
		_, _ = writer.WriteString("{\"type\":\"message\",\"id\":\"first\",\"timestamp\":\"t\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"saved session\"}]}}\n")
	}()
	responsive := make(chan struct{})
	go func() {
		r.Cancel()
		_ = r.HandleEvent(llm.AgentStartEvent{})
		close(responsive)
	}()
	select {
	case <-responsive:
	case <-time.After(time.Second):
		t.Error("session scan held cancellation and event lock")
	}
	var result tui.SessionsMsg
	select {
	case result = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("session scan did not finish")
	}
	if result.Err != nil || len(result.Sessions) != 1 || result.Sessions[0].Path != path || result.Sessions[0].Summary != "saved session" {
		t.Fatalf("sessions = %+v, want saved session", result)
	}
}

// Snapshot 不再扫描会话目录，目录里存在慢速会话也不影响首帧快照
func TestSnapshotDoesNotScanSessions(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	dir, err := session.DefaultDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "slow.jsonl"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, cwd, "main")
	r.Attach(a)
	done := make(chan struct{})
	go func() {
		_ = r.Snapshot()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("snapshot scanned session directory")
	}
}
