package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// killKeyModel 构造一个带单个任务的模型，并记录发出的子任务请求
func killKeyModel(t *testing.T, status string) (*Model, *[]SubagentRequest) {
	t.Helper()
	requests := new([]SubagentRequest)
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Work", Status: status, Description: "edit code",
	}}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		*requests = append(*requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	return m, requests
}

// 选择器聚焦时 x 是命令键，与输入框是否有草稿无关
func TestSelectorKillKeyIgnoresEditorText(t *testing.T) {
	m, requests := killKeyModel(t, "killed")
	typeText(m, "draft")
	m.setTaskFocus(true)
	m.panel.selectedTaskID = "agent-1"
	m.panel.mainSelected = false

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	if len(*requests) != 1 || (*requests)[0].Kind != "evict" || (*requests)[0].TaskID != "agent-1" {
		t.Fatalf("evict request = %#v, want one evict for agent-1", *requests)
	}
	if got := m.editor.Value(); got != "draft" {
		t.Fatalf("editor value = %q, want %q", got, "draft")
	}
}

// 选择器聚焦但没有可执行目标时，x 被吞掉：不落字符也不改焦点
func TestSelectorKillKeySwallowedWithoutTarget(t *testing.T) {
	m, requests := killKeyModel(t, "killed")
	typeText(m, "draft")
	m.setTaskFocus(true)
	m.panel.mainSelected = true
	m.panel.selectedTaskID = ""

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	if len(*requests) != 0 {
		t.Fatalf("requests = %#v, want none", *requests)
	}
	if got := m.editor.Value(); got != "draft" {
		t.Fatalf("editor value = %q, want %q", got, "draft")
	}
	if !m.panel.taskSelector {
		t.Fatal("taskSelector cleared, want focus kept on the task list")
	}
}

// 输入框聚焦且有草稿时 x 是普通字符
func TestEditorXIsPlainCharWithText(t *testing.T) {
	m, requests := killKeyModel(t, "killed")
	m.setTaskFocus(false)
	typeText(m, "a")
	typeText(m, "x")

	if got := m.editor.Value(); got != "ax" {
		t.Fatalf("editor value = %q, want %q", got, "ax")
	}
	if len(*requests) != 0 {
		t.Fatalf("requests = %#v, want none", *requests)
	}
}

// running 任务按 x 走 stop
func TestSelectorKillRunningSendsStop(t *testing.T) {
	m, requests := killKeyModel(t, "running")
	m.setTaskFocus(true)
	m.panel.selectedTaskID = "agent-1"
	m.panel.mainSelected = false

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	if len(*requests) != 1 || (*requests)[0].Kind != "stop" {
		t.Fatalf("request = %#v, want one stop", *requests)
	}
}

// 终态任务按 x 走 evict
func TestSelectorKillKilledSendsEvict(t *testing.T) {
	for _, status := range []string{"killed", "completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			m, requests := killKeyModel(t, status)
			m.setTaskFocus(true)
			m.panel.selectedTaskID = "agent-1"
			m.panel.mainSelected = false

			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

			if len(*requests) != 1 || (*requests)[0].Kind != "evict" {
				t.Fatalf("request = %#v, want one evict", *requests)
			}
		})
	}
}

// removalFallback 优先选被移除项之后第一个幸存者
func TestRemovalFallbackPicksNext(t *testing.T) {
	previous := []SubagentTaskView{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	remaining := []SubagentTaskView{{ID: "a"}, {ID: "c"}}
	got, ok := removalFallback(previous, remaining, "b")
	if !ok || got != "c" {
		t.Fatalf("fallback = %q, %v, want c", got, ok)
	}
}

// 没有后继时回退到前一个幸存者
func TestRemovalFallbackPicksPrevious(t *testing.T) {
	previous := []SubagentTaskView{{ID: "a"}, {ID: "b"}}
	remaining := []SubagentTaskView{{ID: "a"}}
	got, ok := removalFallback(previous, remaining, "b")
	if !ok || got != "a" {
		t.Fatalf("fallback = %q, %v, want a", got, ok)
	}
}

// 全部被移除时没有落点，调用方回到 main 行
func TestRemovalFallbackFallsBackToMain(t *testing.T) {
	previous := []SubagentTaskView{{ID: "a"}}
	if _, ok := removalFallback(previous, nil, "a"); ok {
		t.Fatal("fallback reported a survivor, want none")
	}
}

// 被移除的 ID 不在旧列表里时没有落点
func TestRemovalFallbackUnknownID(t *testing.T) {
	previous := []SubagentTaskView{{ID: "a"}}
	if _, ok := removalFallback(previous, previous, "missing"); ok {
		t.Fatal("fallback reported a survivor for an unknown id, want none")
	}
}
