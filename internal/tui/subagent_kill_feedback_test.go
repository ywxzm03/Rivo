package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func teaWindow(width, height int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: width, Height: height}
}

// kill 后任务行要能一眼看出是被终止的,而不是普通结束
func TestKilledTaskRowIsDistinct(t *testing.T) {
	running := SubagentTaskView{ID: "a1", AgentType: "explore", Status: "running", Description: "victim", StartTime: time.Now()}
	killed := SubagentTaskView{ID: "a1", AgentType: "explore", Status: "killed", Description: "victim", StartTime: time.Now()}
	completed := SubagentTaskView{ID: "a1", AgentType: "explore", Status: "completed", Description: "victim", StartTime: time.Now()}

	model := func(task SubagentTaskView) string {
		m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{task}}}, Handlers{})
		m.Update(teaWindow(100, 16))
		return ansi.Strip(strings.Join(m.taskRows(), "\n"))
	}
	runningRow, killedRow, completedRow := model(running), model(killed), model(completed)
	if killedRow == completedRow {
		t.Fatalf("killed row indistinguishable from completed:\n%s", killedRow)
	}
	if killedRow == runningRow {
		t.Fatalf("killed row indistinguishable from running:\n%s", killedRow)
	}
	lowered := strings.ToLower(killedRow)
	if !strings.Contains(lowered, "killed") && !strings.Contains(lowered, "stopped") {
		t.Fatalf("killed row has no status word:\n%s", killedRow)
	}
}

// 终止全部子任务后,主视图不能残留指向已不存在任务的提示
func TestStopAllClearsTaskPanel(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one", StartTime: time.Now()},
		{ID: "a2", AgentType: "explore", Status: "running", Description: "two", StartTime: time.Now()},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(teaWindow(100, 16))
	m.setTaskFocus(true)
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: nil})
	if m.taskSelector {
		t.Fatal("task focus survived an empty panel")
	}
	if rows := m.taskRows(); len(rows) != 0 {
		t.Fatalf("task rows remain after stop-all: %v", rows)
	}
	if m.viewingTaskID != "" {
		t.Fatalf("viewing task survived: %q", m.viewingTaskID)
	}
}
