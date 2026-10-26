package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// 后台 run 的忙碌态必须能自行结束,否则输入框永远显示旋转器
func TestBackgroundRunEndsBusyState(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo"}, Handlers{})
	m.Update(teaWindow(100, 16))
	m.Update(BackgroundRunMsg{Running: true})
	if !m.busy || m.status != "Working" {
		t.Fatalf("background run did not enter busy state: busy=%v status=%q", m.busy, m.status)
	}
	m.Update(BackgroundRunMsg{Running: false})
	if m.busy || m.status != "" {
		t.Fatalf("background run did not clear busy state: busy=%v status=%q", m.busy, m.status)
	}
}

// 用户操作进行中收到后台 run 的结束消息,不能把用户操作的忙碌态清掉
func TestBackgroundRunEndKeepsUserOperationBusy(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo"}, Handlers{})
	m.Update(teaWindow(100, 16))
	// 用户操作占住忙碌态,后台 run 从未开始
	m.busy, m.status = true, "Working"
	m.Update(BackgroundRunMsg{Running: false})
	if !m.busy {
		t.Fatal("background end message cleared a user operation's busy state")
	}
}

func TestDividerUsesBackgroundWaitingLabel(t *testing.T) {
	cases := []struct {
		name  string
		tasks []SubagentTaskView
		want  string
	}{
		{name: "no background task", want: "Worked for 19s"},
		{name: "agents", tasks: []SubagentTaskView{
			{ID: "a1", Type: "agent", Status: "running"},
			{ID: "a2", Type: "agent", Status: "running"},
		}, want: "Waiting for 2 background agents to finish"},
		{name: "bash", tasks: []SubagentTaskView{
			{ID: "b1", Type: "bash", Status: "running"},
			{ID: "b2", Type: "bash", Status: "running"},
		}, want: "Waiting for 2 background bash to finish"},
		{name: "mixed", tasks: []SubagentTaskView{
			{ID: "a1", Type: "agent", Status: "running"},
			{ID: "b1", Type: "bash", Status: "running"},
		}, want: "Waiting for 2 background tasks to finish"},
		{name: "terminal tasks do not wait", tasks: []SubagentTaskView{
			{ID: "a1", Type: "agent", Status: "completed"},
		}, want: "Worked for 19s"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			m := New(Snapshot{
				Messages:  []Message{{ID: "answer", Role: "assistant", Text: "done", Divider: &TurnDividerView{Elapsed: 19 * time.Second}}},
				Subagents: SubagentSnapshot{Tasks: test.tasks},
			}, Handlers{})
			m.Update(teaWindow(100, 16))
			rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
			if !strings.Contains(rendered, test.want) {
				t.Fatalf("divider = %q, want %q", rendered, test.want)
			}
		})
	}
}

func TestDividerReturnsToWorkedAfterBackgroundTasksFinish(t *testing.T) {
	m := New(Snapshot{
		Messages:  []Message{{ID: "answer", Role: "assistant", Text: "done", Divider: &TurnDividerView{Elapsed: 19 * time.Second}}},
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "a1", Type: "agent", Status: "running"}}},
	}, Handlers{})
	m.Update(teaWindow(100, 16))
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Waiting for 1 background agents to finish") {
		t.Fatalf("running divider = %q", view)
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "a1", Type: "agent", Status: "completed"}}})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Worked for 19s") || strings.Contains(view, "Waiting for") {
		t.Fatalf("finished divider = %q", view)
	}
}

func TestOnlyLatestDividerUsesBackgroundWaitingLabel(t *testing.T) {
	m := New(Snapshot{
		Messages: []Message{
			{ID: "old", Role: "assistant", Text: "old", Divider: &TurnDividerView{Elapsed: time.Second}},
			{ID: "new", Role: "assistant", Text: "new", Divider: &TurnDividerView{Elapsed: 2 * time.Second}},
		},
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "a1", Type: "agent", Status: "running"}}},
	}, Handlers{})
	m.Update(teaWindow(100, 16))
	old := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	latest := ansi.Strip(m.renderMessage(m.state.Messages[1]))
	if strings.Contains(old, "Waiting for") || !strings.Contains(old, "Worked for 1s") {
		t.Fatalf("old divider = %q", old)
	}
	if !strings.Contains(latest, "Waiting for 1 background agents to finish") {
		t.Fatalf("latest divider = %q", latest)
	}
}

func TestBusyMainAgentKeepsWorkingDivider(t *testing.T) {
	m := New(Snapshot{
		Messages:  []Message{{ID: "answer", Role: "assistant", Text: "done", Divider: &TurnDividerView{Elapsed: 19 * time.Second}}},
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "a1", Type: "agent", Status: "running"}}},
	}, Handlers{})
	m.Update(teaWindow(100, 16))
	m.busy = true
	view := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(view, "Worked for 19s") || strings.Contains(view, "Waiting for") {
		t.Fatalf("busy divider = %q", view)
	}
}

func TestOperationDoneStartsSubagentRefreshForRunningTasks(t *testing.T) {
	m := New(Snapshot{}, Handlers{SubagentSnapshot: func() tea.Cmd { return nil }})
	m.Update(teaWindow(100, 16))
	running := Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "a1", Type: "agent", Status: "running"}}}}
	_, cmd := m.Update(OperationDoneMsg{Snapshot: &running})
	if cmd == nil || !m.subagentRefreshRunning {
		t.Fatalf("refresh state = %v, cmd nil=%v", m.subagentRefreshRunning, cmd == nil)
	}
}

// run 期间拉起的子任务必须在 Working 状态下立刻出现在任务列表,
// 不能等到 operation 结束才显示
func TestRunStartsSubagentRefreshAndShowsRowsWhileBusy(t *testing.T) {
	snapshots := 0
	m := New(Snapshot{Cwd: "/repo"}, Handlers{
		Request:          func(Request) tea.Cmd { return nil },
		SubagentSnapshot: func() tea.Cmd { snapshots++; return nil },
	})
	m.Update(teaWindow(120, 24))
	if cmd := m.dispatch("run", "start subagents"); cmd == nil || !m.subagentRefreshRunning {
		t.Fatalf("run did not start subagent refresh: cmd=%v running=%v", cmd != nil, m.subagentRefreshRunning)
	}
	m.applySubagentSnapshot(SubagentSnapshot{Revision: 1, Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "gin-demo"},
	}})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "gin-demo") || !strings.Contains(view, "Working") {
		t.Fatalf("busy view missing subagent row:\n%s", view)
	}
	// 任务还在跑,刷新链必须继续轮询
	m.Update(subagentRefreshMsg{})
	if snapshots == 0 {
		t.Fatal("refresh chain stopped while the main agent was still running")
	}
}

func TestBackgroundRunKeepsSubagentRowsVisible(t *testing.T) {
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "work", Status: "running", Description: "two"},
	}}}, Handlers{})
	m.Update(teaWindow(120, 12))
	m.Update(BackgroundRunMsg{Running: true})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "one") || !strings.Contains(view, "two") || !strings.Contains(view, "Working") {
		t.Fatalf("background run swallowed task rows:\n%s", view)
	}
}
