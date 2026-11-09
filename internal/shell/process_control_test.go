//go:build !windows

package shell

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// FindBash 在 POSIX 上必须返回可执行的 /bin/bash
func TestFindBashReturnsExecutable(t *testing.T) {
	path, err := FindBash()
	if err != nil {
		t.Fatalf("FindBash() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		t.Fatalf("FindBash() = %q, want an executable file", path)
	}
}

// ConfigureProcess 必须把进程放进独立进程组，并设置等待上限
func TestConfigureProcessSetsProcessGroup(t *testing.T) {
	cmd := exec.Command("true")
	ConfigureProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("SysProcAttr.Setpgid = false, want a dedicated process group")
	}
	if cmd.WaitDelay != 500*time.Millisecond {
		t.Errorf("WaitDelay = %v, want 500ms", cmd.WaitDelay)
	}
	if cmd.Cancel == nil {
		t.Fatal("Cancel = nil, want a cancel hook")
	}
	// 未启动时取消必须安全返回
	if err := cmd.Cancel(); err != nil {
		t.Errorf("Cancel() before start = %v, want nil", err)
	}
}

// KillProcessTree 必须杀掉整组进程；进程未启动时也要安全
func TestKillProcessTreeStopsGroup(t *testing.T) {
	notStarted := exec.Command("true")
	KillProcessTree(notStarted)

	// ConfigureProcess 会设置 Cancel，必须用 CommandContext 创建
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	ConfigureProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	KillProcessTree(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("killed process did not exit")
	}
}
