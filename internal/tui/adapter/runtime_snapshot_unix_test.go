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

func TestSessionScanDoesNotBlockRuntime(t *testing.T) {
	for _, kind := range []string{"snapshot", "completion"} {
		t.Run(kind, func(t *testing.T) {
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
			snapshot := r.Snapshot
			if kind == "completion" {
				cmd := r.Request(tui.Request{ID: 1, Kind: "thinking", Value: "off"})
				snapshot = func() tui.Snapshot { return *cmd().(tui.OperationDoneMsg).Snapshot }
			}
			finished := make(chan tui.Snapshot, 1)
			go func() { finished <- snapshot() }()
			// 等待扫描器打开管道，再暂缓内容写入以模拟慢速会话读取
			writer, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
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
			if _, err := writer.WriteString("{\"type\":\"message\",\"id\":\"first\",\"timestamp\":\"t\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"saved session\"}]}}\n"); err != nil {
				t.Fatal(err)
			}
			_ = writer.Close()
			result := <-finished
			<-responsive
			if len(result.Sessions) != 1 || result.Sessions[0].Path != path || result.Sessions[0].Summary != "saved session" {
				t.Fatalf("snapshot sessions = %+v, want saved session", result.Sessions)
			}
		})
	}
}
