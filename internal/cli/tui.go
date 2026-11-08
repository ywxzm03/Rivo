package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"

	textclipboard "github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/gitmeta"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/tui"
	"github.com/ywxzm03/Rivo/internal/tui/adapter"
)

func runTUI(cfg provider.Config, target sessionTarget, initialPrompt string, stdin, stdout *os.File, stderr io.Writer) (exitCode int) {
	// panic 会跳过所有收尾逻辑并让进程直接消失;兜住它,并把子任务一起收掉
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(stderr, "Rivo: internal error: %v\n%s\n", recovered, debug.Stack())
			exitCode = 1
		}
	}()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: failed to get working directory: %v\n", err)
		return 2
	}
	branch := gitBranch(cwd)
	runtime := adapter.New(cfg, cwd, branch)
	agentDir, err := config.Dir()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: input history directory: %v\n", err)
		return 2
	}
	history := app.NewInputHistory(filepath.Join(agentDir, "history.jsonl"), runtime.NotifyHistoryFailure)
	runtime.BindInputHistory(history)
	defer func() {
		runtime.Cancel()
		runtime.Wait()
		runtime.ShutdownSubagents()
		runtime.Close()
		for _, failure := range history.Close() {
			fmt.Fprintf(stderr, "Rivo: input history: %v\n", failure)
		}
	}()
	a, err := openSessionTarget(cfg, target, runtime.HandleEvent)
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	if a.Cwd() != cwd {
		branch = gitBranch(a.Cwd())
	}
	runtime.SetProjectContext(a.Cwd(), branch)
	if msg := a.ModelFallbackMessage(); msg != "" {
		fmt.Fprintf(stderr, "Rivo: %s\n", msg)
	}
	runtime.Attach(a)
	output := newSerializedOutput(stdout)
	model := tui.NewWithInitialPrompt(runtime.Snapshot(), tui.Handlers{
		Request:                 runtime.Request,
		Status:                  runtime.Status,
		Cancel:                  runtime.Cancel,
		EnqueueSteeringMessage:  runtime.EnqueueSteeringMessage,
		RestoreSteeringMessages: runtime.RestoreSteeringMessages,
		EnqueueFollowUpMessage:  runtime.EnqueueFollowUpMessage,
		RestoreFollowUpMessages: runtime.RestoreFollowUpMessages,
		ClearFollowUp:           runtime.ClearFollowUp,
		ReadClipboardData:       readClipboardData,
		SubagentRequest:         runtime.SubagentRequest,
		SubagentSnapshot:        runtime.SubagentSnapshot,
		Sessions:                runtime.Sessions,
		ShellList:               runtime.ShellList,
		ShellKill:               runtime.ShellKill,
		ShellStopAll:            runtime.ShellStopAll,
		History:                 runtime.HistoryHandlers(),
		WriteClipboard: func(text string) error {
			if err := textclipboard.WriteAll(text); err == nil {
				return nil
			}
			return writeOSC52(output, text)
		},
	}, initialPrompt)
	input := tui.NewInputReader(stdin)
	defer input.Close()
	program := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(tui.NewCursorWriter(output, model.CursorPosition)), tea.WithAltScreen(), tea.WithMouseAllMotion())
	runtime.SetProgram(program)
	_, err = program.Run()
	runtime.Cancel()
	runtime.Wait()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		// Bubble Tea 把 goroutine 内的 panic 转成 ErrProgramPanic,
		// 原始堆栈只写在它自己的日志里;提示用户去哪里找
		if errors.Is(err, tea.ErrProgramPanic) {
			fmt.Fprintf(stderr, "Rivo: 程序内部发生 panic，堆栈见 Bubble Tea 日志（$TMPDIR 下的 bubbletea 日志）\n")
		}
		return 1
	}
	return 0
}

// gitBranch 优先直接读 .git/HEAD；git 环境变量会改变仓库发现，交给子进程
func gitBranch(cwd string) string {
	if os.Getenv("GIT_DIR") != "" || os.Getenv("GIT_WORK_TREE") != "" ||
		os.Getenv("GIT_CEILING_DIRECTORIES") != "" || os.Getenv("GIT_DISCOVERY_ACROSS_FILESYSTEM") != "" {
		return gitBranchCommand(cwd)
	}
	return gitmeta.CurrentBranch(cwd)
}

// gitBranchCommand 在 git 环境变量覆盖仓库发现时兜底
func gitBranchCommand(cwd string) string {
	branchBytes, _ := exec.Command("git", "-C", cwd, "branch", "--show-current").Output()
	return strings.TrimSpace(string(branchBytes))
}

// serializedOutput 串行化 Bubble Tea 渲染与 OSC52 fallback 的终端写入
type serializedOutput struct {
	mu   sync.Mutex
	file *os.File
}

func newSerializedOutput(file *os.File) *serializedOutput {
	return &serializedOutput{file: file}
}

func (w *serializedOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, io.ErrClosedPipe
	}
	return w.file.Write(data)
}

func (w *serializedOutput) Read(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, io.ErrClosedPipe
	}
	return w.file.Read(data)
}

func (w *serializedOutput) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

func (w *serializedOutput) Fd() uintptr {
	if w.file == nil {
		return 0
	}
	return w.file.Fd()
}

func writeOSC52(stdout interface {
	io.Writer
	Fd() uintptr
}, text string) error {
	if stdout == nil || !term.IsTerminal(stdout.Fd()) {
		return fmt.Errorf("clipboard: terminal output unavailable")
	}
	_, err := fmt.Fprint(stdout, osc52.New(text))
	return err
}
