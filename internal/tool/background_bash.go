package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/task"
)

type backgroundBashRunner struct {
	store *task.Store
}

const backgroundMaxOutputBytes = defaultMaxBytes

func NewBackgroundBashRunner(store *task.Store) BackgroundRunner {
	return &backgroundBashRunner{store: store}
}

func (r *backgroundBashRunner) RunBackground(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	command, timeout, err := parseShellArgs(args, "bash")
	if err != nil {
		return llm.ToolResult{}, err
	}
	bashPath, err := findBash()
	if err != nil {
		return llm.ToolResult{}, err
	}
	output, err := os.CreateTemp("", "rivo-bash-*.log")
	if err != nil {
		return llm.ToolResult{}, err
	}
	path := output.Name()
	commandCtx, cancel := context.WithCancel(context.Background())
	if timeout > 0 {
		commandCtx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	cmd := exec.CommandContext(commandCtx, bashPath, "-c", command)
	cwd := llm.CurrentWorkingDir(ctx)
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			cancel()
			_ = output.Close()
			_ = os.Remove(path)
			return llm.ToolResult{}, err
		}
	}
	cmd.Dir = cwd
	cmd.Env = shellEnvironment()
	configureShellProcess(cmd)
	backgroundOutput := &limitedBackgroundOutput{file: output, max: backgroundMaxOutputBytes}
	cmd.Stdout = backgroundOutput
	cmd.Stderr = backgroundOutput
	if err := cmd.Start(); err != nil {
		cancel()
		_ = output.Close()
		_ = os.Remove(path)
		return llm.ToolResult{}, err
	}
	process := &backgroundProcess{cmd: cmd, file: output, capture: backgroundOutput, cancel: cancel}
	// 记录发起者,子 agent 结束时据此回收它留下的孤儿进程
	parentAgentID, _ := llm.CurrentToolContext(ctx)
	id, token, err := r.store.Register(task.RegisterSpec{
		Type:        task.TypeBash,
		Description: command,
		OutputFile:  path,
		BashState: &task.BashState{
			Command:       command,
			Process:       process,
			ParentAgentID: parentAgentID.AgentID,
		},
	})
	if err != nil {
		_ = process.Kill()
		_ = process.Cleanup()
		_ = os.Remove(path)
		return llm.ToolResult{}, err
	}
	go waitBackgroundBash(commandCtx, r.store, id, token, process, cmd)
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: fmt.Sprintf("Started background task %s", id)}}, Details: map[string]any{"task_id": id, "output_file": path}}, nil
}

type backgroundProcess struct {
	cmd     *exec.Cmd
	file    *os.File
	capture *limitedBackgroundOutput
	cancel  context.CancelFunc
	once    sync.Once
}

func (p *backgroundProcess) Kill() error {
	var err error
	if p.cmd.Cancel != nil {
		err = p.cmd.Cancel()
	}
	p.cancel()
	if err == os.ErrProcessDone {
		return nil
	}
	return err
}

func (p *backgroundProcess) Cleanup() error {
	var err error
	p.once.Do(func() { err = p.file.Close() })
	return err
}

func waitBackgroundBash(ctx context.Context, store *task.Store, id string, token uint64, process *backgroundProcess, cmd *exec.Cmd) {
	defer process.cancel()
	err := cmd.Wait()
	_ = process.Cleanup()
	truncated, bytes := process.capture.Stats()
	store.SetBashOutputStats(id, token, truncated, bytes)
	// 输出路径在任务被驱逐时随记录一起消失,通知里要自带一份
	path := outputFile(store, id)
	notification := func(status task.Status, summary string) task.Notification {
		return task.Notification{TaskID: id, Status: status, OutputFile: path, Summary: summary, OutputTruncated: truncated, OutputBytes: bytes}
	}
	if ctx.Err() != nil {
		store.FailBash(id, token, &task.BashResult{Code: -1, Interrupted: true}, ctx.Err())
		store.EnqueueNotification(id, token, notification(task.StatusFailed, "background bash interrupted"))
		return
	}
	result := &task.BashResult{Code: 0}
	if err != nil {
		result.Code = exitCode(err)
		store.FailBash(id, token, result, err)
		store.EnqueueNotification(id, token, notification(task.StatusFailed, "background bash failed"))
		return
	}
	store.CompleteBash(id, token, result)
	store.EnqueueNotification(id, token, notification(task.StatusCompleted, "background bash completed"))
}

type limitedBackgroundOutput struct {
	mu        sync.Mutex
	file      *os.File
	max       int
	used      int
	truncated bool
}

func (w *limitedBackgroundOutput) Stats() (bool, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated, w.used
}

func (w *limitedBackgroundOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.used >= w.max {
		// 还有字节要写但已到上限,说明确实丢过输出
		w.truncated = true
		return len(data), nil
	}
	keep := len(data)
	if remaining := w.max - w.used; keep > remaining {
		keep = remaining
		w.truncated = true
	}
	if _, err := w.file.Write(data[:keep]); err != nil {
		return 0, err
	}
	w.used += keep
	return len(data), nil
}

func outputFile(store *task.Store, id string) string {
	task, ok := store.Task(id)
	if !ok {
		return ""
	}
	return task.Base().OutputFile
}

func exitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}
