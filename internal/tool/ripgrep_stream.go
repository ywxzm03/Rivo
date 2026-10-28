package tool

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"

	"github.com/ywxzm03/Rivo/internal/shell"
)

// runRipgrepStream 是 grep 与 find 共用的 rg 子进程管线：
// 启动进程、按行回调、达到上限时杀掉整棵进程树、等待并解释退出码。
// onLine 返回 true 表示调用方已达到上限，管线会终止进程
func runRipgrepStream(ctx context.Context, rgPath, dir string, args []string, onLine func(line []byte) bool) (stopped bool, err error) {
	command := exec.CommandContext(ctx, rgPath, args...)
	command.Dir = dir
	stdout, err := command.StdoutPipe()
	if err != nil {
		return false, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	shell.ConfigureProcess(command)
	if err := command.Start(); err != nil {
		return false, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if onLine(scanner.Bytes()) {
			stopped = true
			shell.KillProcessTree(command)
			break
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		if !stopped {
			shell.KillProcessTree(command)
		}
		_ = command.Wait()
		return stopped, scanErr
	}
	waitErr := command.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stopped, ctxErr
	}
	return stopped, ripgrepExitError(waitErr, stderr.String(), stopped)
}
