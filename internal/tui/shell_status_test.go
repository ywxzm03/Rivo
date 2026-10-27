package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// 提示行按计数切换单复数，计数为 0 时不渲染
func TestBackgroundShellStatusLine(t *testing.T) {
	cases := []struct {
		running int
		want    string
	}{
		{running: 0, want: ""},
		{running: -1, want: ""},
		{running: 1, want: "1 shell still running · /ps to view · /stop to close"},
		{running: 2, want: "2 shells still running · /ps to view · /stop to close"},
	}
	for _, test := range cases {
		if got := backgroundShellStatusLine(test.running); got != test.want {
			t.Errorf("backgroundShellStatusLine(%d) = %q, want %q", test.running, got, test.want)
		}
	}
}

// /stop 受理文案
func TestShellStopLine(t *testing.T) {
	if got := shellStopLine(2, nil); got != "Stopping all background shells." {
		t.Errorf("shellStopLine(2) = %q", got)
	}
	if got := shellStopLine(0, nil); got != "No background shells to stop." {
		t.Errorf("shellStopLine(0) = %q", got)
	}
	if got := shellStopLine(0, errors.New("boom")); !strings.Contains(got, "boom") {
		t.Errorf("shellStopLine with error = %q, want it to carry the error", got)
	}
}

// /ps 结果写进 transcript，主 agent 运行中也能看到
func TestShellsMsgAppendsPanel(t *testing.T) {
	m := testModel(100, 20)
	m.Update(ShellsMsg{Shells: []ShellView{{Command: "sleep 120", Status: "running"}}})
	view := m.View()
	if !strings.Contains(view, "Background shells") || !strings.Contains(view, "sleep 120") {
		t.Fatalf("view = %q, want the shell panel", view)
	}
}

// /stop 结果以 notice 呈现
func TestShellsStoppedMsgNotices(t *testing.T) {
	m := testModel(100, 20)
	m.Update(ShellsStoppedMsg{Stopped: 1})
	if view := m.View(); !strings.Contains(view, "Stopping all background shells.") {
		t.Fatalf("view = %q, want the stop notice", view)
	}
}

// 提示行渲染在输入框上方
func TestShellStatusRendersAboveEditor(t *testing.T) {
	m := testModel(100, 20)
	m.state.Subagents.RunningShells = 1
	view := m.View()
	statusIndex := strings.Index(view, "1 shell still running")
	if statusIndex < 0 {
		t.Fatalf("view = %q, want the shell status line", view)
	}
	editorIndex := strings.Index(view, m.editor.View())
	if editorIndex < 0 || statusIndex > editorIndex {
		t.Fatalf("shell status line is not above the editor (status=%d editor=%d)", statusIndex, editorIndex)
	}
}

// /stop 之后立刻拉一次快照，计数不等下一轮轮询
func TestShellsStoppedMsgTriggersSnapshot(t *testing.T) {
	snapshots := 0
	m := New(Snapshot{Cwd: "/repo"}, Handlers{SubagentSnapshot: func() tea.Cmd {
		snapshots++
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.Update(ShellsStoppedMsg{Stopped: 1})
	if snapshots != 1 {
		t.Fatalf("snapshot requests = %d, want 1", snapshots)
	}
}

// 有运行中的 shell 时必须继续轮询，否则提示行计数不会更新
func TestNeedsSubagentRefreshCountsRunningShells(t *testing.T) {
	m := testModel(100, 20)
	if m.needsSubagentRefresh() {
		t.Fatal("refresh needed without tasks or shells")
	}
	m.state.Subagents.RunningShells = 1
	if !m.needsSubagentRefresh() {
		t.Fatal("refresh not needed with a running shell")
	}
}
