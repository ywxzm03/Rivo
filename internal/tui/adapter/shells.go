package adapter

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// shellOutputTailLines 是 /ps 面板里每个 shell 保留的输出行数
const shellOutputTailLines = 20

// shellViews 把 store 里的后台 bash 任务投影成 /ps 面板数据
func (r *Runtime) shellViews(states []task.State) []tui.ShellView {
	shells := make([]tui.ShellView, 0, len(states))
	for _, state := range states {
		bash, ok := state.(*task.BashState)
		if !ok || bash.Base() == nil {
			continue
		}
		base := bash.Base()
		view := tui.ShellView{Command: bash.Command, Status: string(base.Status)}
		if base.OutputFile != "" {
			tail, truncated, err := readShellOutputTail(base.OutputFile, shellOutputTailLines)
			view.OutputTail, view.Truncated = tail, truncated
			if err != nil {
				view.OutputError = err.Error()
			}
		}
		if bash.Result != nil && bash.Result.Code != 0 {
			view.OutputError = "exit code " + strconv.Itoa(bash.Result.Code)
		}
		shells = append(shells, view)
	}
	return shells
}

// readShellOutputTail 读取输出文件末尾若干行,只保留环形缓冲里最后 limit 行
func readShellOutputTail(path string, limit int) ([]string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	ring := make([]string, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if len(ring) == limit {
			copy(ring, ring[1:])
			ring = ring[:limit-1]
		}
		ring = append(ring, strings.TrimRight(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return ring, false, err
	}
	return ring, len(ring) == limit, nil
}

// ShellList 是 /ps 的查询入口,读一次 store 快照并投影成面板数据
func (r *Runtime) ShellList() tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		activeApp := r.app
		r.mu.Unlock()
		if activeApp == nil || activeApp.SubagentStore() == nil {
			return tui.ShellsMsg{Err: app.ErrSteeringUnavailable}
		}
		return tui.ShellsMsg{Shells: r.shellViews(activeApp.SubagentStore().Snapshot())}
	}
}

// ShellStopAll 是 /stop 的执行入口,终止全部运行中的后台 shell
func (r *Runtime) ShellStopAll() tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		activeApp := r.app
		r.mu.Unlock()
		if activeApp == nil || activeApp.SubagentStore() == nil {
			return tui.ShellsStoppedMsg{Err: app.ErrSteeringUnavailable}
		}
		store := activeApp.SubagentStore()
		stopped := 0
		for _, state := range store.Snapshot() {
			if bash, ok := state.(*task.BashState); ok && bash.Base().Status == task.StatusRunning {
				stopped++
			}
		}
		store.KillAllRunningTasks()
		return tui.ShellsStoppedMsg{Stopped: stopped}
	}
}
