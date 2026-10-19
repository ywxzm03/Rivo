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

func runTUI(cfg app.Config, continueRecent bool, stdin, stdout *os.File, stderr io.Writer) int {
	cwd, _ := os.Getwd()
	branchBytes, _ := exec.Command("git", "branch", "--show-current").Output()
	runtime := adapter.New(cfg, cwd, strings.TrimSpace(string(branchBytes)))
	var a *app.App
	var err error
	if continueRecent {
		a, err = resumeRecentApp(cfg, runtime.HandleEvent)
	} else {
		a, err = app.New(cfg, runtime.HandleEvent)
	}
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	if msg := a.ModelFallbackMessage(); msg != "" {
		fmt.Fprintf(stderr, "Rivo: %s\n", msg)
	}
	runtime.Attach(a)
	output := newSerializedOutput(stdout)
	model := tui.New(runtime.Snapshot(), tui.Handlers{
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
	})
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
