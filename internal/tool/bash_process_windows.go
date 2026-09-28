//go:build windows

package tool

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// findBash 按 PATH 和 Git for Windows 的默认安装位置查找 bash.exe
func findBash() (string, error) {
	if path, err := exec.LookPath("bash.exe"); err == nil {
		return path, nil
	}
	candidates := make([]string, 0, 8)
	if root := os.Getenv("GIT_INSTALL_ROOT"); root != "" {
		candidates = append(candidates,
			filepath.Join(root, "bin", "bash.exe"),
			filepath.Join(root, "usr", "bin", "bash.exe"),
		)
	}
	roots := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")}
	if localAppData := os.Getenv("LocalAppData"); localAppData != "" {
		roots = append(roots, filepath.Join(localAppData, "Programs"))
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(root, "Git", "bin", "bash.exe"),
			filepath.Join(root, "Git", "usr", "bin", "bash.exe"),
		)
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("bash executable not found. Install Git for Windows or add bash.exe to PATH")
}

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
