package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	textclipboard "github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/tui"
	"github.com/ywxzm03/Rivo/internal/tui/adapter"
)

func runTUI(cfg app.Config, target sessionTarget, initialPrompt string, stdin, stdout *os.File, stderr io.Writer) int {
	cwd, _ := os.Getwd()
	runtime := adapter.New(cfg, cwd, gitBranch(cwd))
	a, err := openSessionTarget(cfg, target, runtime.HandleEvent)
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	runtime.SetProjectContext(a.Cwd(), gitBranch(a.Cwd()))
	if msg := a.ModelFallbackMessage(); msg != "" {
		fmt.Fprintf(stderr, "Rivo: %s\n", msg)
	}
	runtime.Attach(a)
	output := newSerializedOutput(stdout)
	model := tui.NewWithInitialPrompt(runtime.Snapshot(), tui.Handlers{
		Request:           runtime.Request,
		Context:           runtime.Context,
		Cancel:            runtime.Cancel,
		EnqueueSteering:   runtime.EnqueueSteering,
		RestoreSteering:   runtime.RestoreSteering,
		ReadClipboardData: readClipboardData,
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
		return 1
	}
	return 0
}

func gitBranch(cwd string) string {
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
