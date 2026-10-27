package adapter

import (
	"bufio"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// shellOutputTailLines 是 /ps 弹窗里每个 shell 保留的输出行数
const shellOutputTailLines = 20

// shellViews 把 store 里的后台 bash 任务投影成 /ps 弹窗数据。
// store 快照来自 map 遍历,顺序随机,必须按启动时间排好,
// 否则每次刷新列表都会重排,选中项跟着漂移
func (r *Runtime) shellViews(states []task.State) []tui.ShellView {
	shells := make([]tui.ShellView, 0, len(states))
	for _, state := range states {
		bash, ok := state.(*task.BashState)
		if !ok || bash.Base() == nil {
			continue
		}
		base := bash.Base()
		view := tui.ShellView{
			ID:        base.ID,
			Command:   bash.Command,
			Status:    string(base.Status),
			StartTime: base.StartTime,
			EndTime:   base.EndTime,
		}
		if bash.Result != nil {
			code := bash.Result.Code
			view.ExitCode = &code
		}
		if base.OutputFile != "" {
			tail, truncated, bytes, err := readShellOutputTail(base.OutputFile, shellOutputTailLines)
			view.OutputTail, view.Truncated, view.OutputBytes = tail, truncated, bytes
			if err != nil {
				view.OutputError = err.Error()
			}
		}
		shells = append(shells, view)
	}
	sort.SliceStable(shells, func(i, j int) bool {
		if !shells[i].StartTime.Equal(shells[j].StartTime) {
			return shells[i].StartTime.Before(shells[j].StartTime)
		}
		return shells[i].ID < shells[j].ID
	})
	return shells
}

// readShellOutputTail 读取输出文件末尾若干行,只保留环形缓冲里最后 limit 行,
// 同时返回文件总字节数,供弹窗展示被截断的规模
func readShellOutputTail(path string, limit int) ([]string, bool, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, 0, err
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
		return ring, false, 0, err
	}
	var size int64
	if info, statErr := file.Stat(); statErr == nil {
		size = info.Size()
	}
	return ring, len(ring) == limit, size, nil
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

// ShellKill 是 /ps 弹窗内终止单个 shell 的执行入口
func (r *Runtime) ShellKill(id string) tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		activeApp := r.app
		r.mu.Unlock()
		if activeApp == nil || activeApp.SubagentStore() == nil {
			return tui.ShellKilledMsg{ID: id, Err: app.ErrSteeringUnavailable}
		}
		return tui.ShellKilledMsg{ID: id, Err: task.KillBash(id, activeApp.SubagentStore())}
	}
}

// ShellStopAll 是 /stop 的执行入口,终止全部运行中的后台 shell。
// 只停 bash:子 agent 的取消走各自的取消链路,不能被 /stop 连带终止
func (r *Runtime) ShellStopAll() tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		activeApp := r.app
		r.mu.Unlock()
		if activeApp == nil || activeApp.SubagentStore() == nil {
			return tui.ShellsStoppedMsg{Err: app.ErrSteeringUnavailable}
		}
		return tui.ShellsStoppedMsg{Stopped: activeApp.SubagentStore().KillAllRunningBashTasks()}
	}
}
