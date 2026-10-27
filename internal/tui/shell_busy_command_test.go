package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// runningModel 构造一个 busy（agent 运行中）的模型
func runningModel(t *testing.T) (*Model, *int, *int) {
	t.Helper()
	listCalls, stopCalls := new(int), new(int)
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{RunningShells: 1}}, Handlers{
		Request: func(Request) tea.Cmd { return nil },
		ShellList: func() tea.Cmd {
			*listCalls++
			return nil
		},
		ShellStopAll: func() tea.Cmd {
			*stopCalls++
			return nil
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.busy = true
	m.operationIsRun = true
	return m, listCalls, stopCalls
}

// agent 运行中 /ps 仍要执行
func TestPsRunsWhileAgentIsBusy(t *testing.T) {
	m, listCalls, _ := runningModel(t)
	m.editor.SetValue("/ps")
	m.submit()

	if *listCalls != 1 {
		t.Fatalf("/ps handler calls = %d, want 1 while the agent is running", *listCalls)
	}
	if got := m.editor.Value(); got != "" {
		t.Fatalf("editor value = %q, want the command consumed", got)
	}
}

// agent 运行中 /stop 仍要执行
func TestStopRunsWhileAgentIsBusy(t *testing.T) {
	m, _, stopCalls := runningModel(t)
	m.editor.SetValue("/stop")
	m.submit()

	if *stopCalls != 1 {
		t.Fatalf("/stop handler calls = %d, want 1 while the agent is running", *stopCalls)
	}
}

// 运行中执行后不得出现「命令不可用」的提示
func TestShellCommandsDoNotWarnWhileBusy(t *testing.T) {
	for _, command := range []string{"/ps", "/stop"} {
		m, _, _ := runningModel(t)
		m.editor.SetValue(command)
		m.submit()

		for _, message := range m.state.Messages {
			if message.Role == "notice" && strings.Contains(message.Text, "unavailable") {
				t.Fatalf("%s produced %q, want no unavailable notice", command, message.Text)
			}
		}
	}
}

// 运行中的普通输入仍然进队列，不能因为放开命令而被当成命令执行
func TestPlainInputStillQueuesWhileBusy(t *testing.T) {
	m, listCalls, stopCalls := runningModel(t)
	m.editor.SetValue("hello")
	m.submit()

	if *listCalls != 0 || *stopCalls != 0 {
		t.Fatal("plain input triggered a shell handler")
	}
}

// 运行中其他命令仍被拦截，保持既有语义
func TestOtherCommandsStillBlockedWhileBusy(t *testing.T) {
	m, _, _ := runningModel(t)
	m.editor.SetValue("/compact")
	m.submit()

	found := false
	for _, message := range m.state.Messages {
		if message.Role == "notice" && strings.Contains(message.Text, "unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatal("/compact not blocked while the agent is running")
	}
}

// 压缩期间同样允许这两个命令
func TestShellCommandsRunDuringCompaction(t *testing.T) {
	for _, command := range []string{"/ps", "/stop"} {
		m, listCalls, stopCalls := runningModel(t)
		m.compacting = true
		m.editor.SetValue(command)
		m.submit()

		if *listCalls+*stopCalls != 1 {
			t.Fatalf("%s did not run during compaction", command)
		}
	}
}
