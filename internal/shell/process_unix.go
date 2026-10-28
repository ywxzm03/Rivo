//go:build !windows

package shell

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func FindBash() (string, error) {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash", nil
	}
	return "", fmt.Errorf("bash executable not found at /bin/bash")
}

func ConfigureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 500 * time.Millisecond
}

func KillProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
