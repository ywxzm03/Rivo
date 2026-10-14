package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
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
	runtime.Attach(a)
	model := tui.New(runtime.Snapshot(), tui.Handlers{
		Request:       runtime.Request,
		Cancel:        runtime.Cancel,
		ReadClipboard: clipboard.ReadAll,
		WriteClipboard: func(text string) error {
			if err := clipboard.WriteAll(text); err == nil {
				return nil
			}
			_, err := fmt.Fprint(stderr, osc52.New(text))
			return err
		},
	})
	program := tea.NewProgram(model, tea.WithInput(tui.NewInputReader(stdin)), tea.WithOutput(tui.NewCursorWriter(stdout, model.CursorPosition)), tea.WithAltScreen(), tea.WithMouseCellMotion())
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
