//go:build windows

package tool

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func configureShellProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = killShellProcess(cmd.Process.Pid)
		return os.ErrProcessDone
	}
	cmd.WaitDelay = 500 * time.Millisecond
}

func killShellProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = killShellProcess(cmd.Process.Pid)
	}
}

// killShellProcess 使用 taskkill 递归终止 shell 及其子进程
func killShellProcess(pid int) error {
	return newTaskkillCommand(pid).Run()
}

func newTaskkillCommand(pid int) *exec.Cmd {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	command := exec.Command(
		filepath.Join(root, "System32", "taskkill.exe"),
		"/F", "/T", "/PID", strconv.Itoa(pid),
	)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command
}
