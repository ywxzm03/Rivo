package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// focusModel 构造一个带单个任务的模型，焦点初始在输入框
func focusModel(t *testing.T) *Model {
	t.Helper()
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Work", Status: "running", Description: "edit code", StartTime: time.Now(),
	}}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	return m
}

// assertFocusInvariant 检查焦点与意图互为反面
func assertFocusInvariant(t *testing.T, m *Model, where string) {
	t.Helper()
	if m.composer.selector != nil {
		return
	}
	if m.panel.taskSelector == m.editor.Focused() {
		t.Fatalf("%s: taskSelector=%v editorFocused=%v, want opposite",
			where, m.panel.taskSelector, m.editor.Focused())
	}
}

// 主视图按 Left 聚焦列表后 Enter 落在 main 行，必须把焦点交回输入框
func TestEnterMainRowRestoresEditorFocus(t *testing.T) {
	m := focusModel(t)
	m.setTaskFocus(true)
	m.panel.mainSelected = true
	m.panel.selectedTaskID = ""

	m.enterSelectedSubagent()

	assertFocusInvariant(t, m, "after entering main row")
	if !m.editor.Focused() {
		t.Fatal("editor not focused after selecting the main row")
	}
	typeText(m, "hello")
	if got := m.editor.Value(); got != "hello" {
		t.Fatalf("editor value = %q, want %q", got, "hello")
	}
}

// 退出子任务视图后输入框必须可输入
func TestExitSubagentViewRestoresFocus(t *testing.T) {
	m := focusModel(t)
	m.enterSubagentView("agent-1")
	m.exitSubagentView()

	assertFocusInvariant(t, m, "after exiting subagent view")
	if !m.editor.Focused() {
		t.Fatal("editor not focused after exiting the subagent view")
	}
	typeText(m, "hi")
	if got := m.editor.Value(); got != "hi" {
		t.Fatalf("editor value = %q, want %q", got, "hi")
	}
}

// 任务列表被清空时,焦点必须回到输入框
func TestEmptyTaskListClearsSelectorFocus(t *testing.T) {
	m := focusModel(t)
	m.setTaskFocus(true)
	if !m.panel.taskSelector {
		t.Fatal("setup: selector not focused")
	}

	m.applySubagentSnapshot(SubagentSnapshot{})

	assertFocusInvariant(t, m, "after the task list emptied")
	if m.panel.taskSelector {
		t.Fatal("taskSelector still true with an empty list")
	}
	if !m.editor.Focused() {
		t.Fatal("editor not focused after the task list emptied")
	}
}

// 输入框意外失焦时,打一个字符即可自愈
func TestTypingHealsLostFocus(t *testing.T) {
	m := focusModel(t)
	m.panel.taskSelector = false
	m.editor.Blur()

	typeText(m, "a")

	if got := m.editor.Value(); got != "a" {
		t.Fatalf("editor value = %q, want %q", got, "a")
	}
}

// setTaskFocus 意图未变时仍要把焦点纠正回来
func TestSetTaskFocusAlwaysApplies(t *testing.T) {
	m := focusModel(t)
	m.panel.taskSelector = false
	m.editor.Blur()

	m.setTaskFocus(false)

	if !m.editor.Focused() {
		t.Fatal("setTaskFocus(false) did not restore the editor focus")
	}
}

// 切换会话时 panel 必须清空
func TestResetSessionUIClearsPanel(t *testing.T) {
	m := focusModel(t)
	m.panel.viewingTaskID = "agent-1"
	m.panel.taskSelector = true
	m.panel.selectedTaskID = "agent-1"
	m.panel.mainMessages = []Message{{ID: "x"}}

	m.resetSessionUI()

	if m.panel.viewingTaskID != "" || m.panel.taskSelector || m.panel.selectedTaskID != "" || len(m.panel.mainMessages) != 0 {
		t.Fatalf("panel not cleared: %#v", m.panel)
	}
}

// 选择器（补全）打开时 applyKeyboardFocus 不改动焦点
func TestApplyKeyboardFocusSkipsSelector(t *testing.T) {
	m := focusModel(t)
	m.composer.selector = &selector{}
	focusedBefore := m.editor.Focused()

	m.panel.taskSelector = true
	m.applyKeyboardFocus()

	if m.editor.Focused() != focusedBefore {
		t.Fatal("applyKeyboardFocus changed focus while a selector is open")
	}
}

// 各入口走一遍后不变式仍成立
func TestFocusInvariant(t *testing.T) {
	m := focusModel(t)
	assertFocusInvariant(t, m, "initial")

	m.setTaskFocus(true)
	assertFocusInvariant(t, m, "after focusing the list")

	m.enterSubagentView("agent-1")
	assertFocusInvariant(t, m, "after entering the view")

	m.exitSubagentView()
	assertFocusInvariant(t, m, "after exiting the view")

	m.setTaskFocus(true)
	m.panel.mainSelected = true
	m.enterSelectedSubagent()
	assertFocusInvariant(t, m, "after selecting main")

	m.applySubagentSnapshot(SubagentSnapshot{})
	assertFocusInvariant(t, m, "after an empty snapshot")
}

// 整份快照替换把任务列表清空时,焦点也必须回到输入框
func TestApplySnapshotClearsSelectorFocus(t *testing.T) {
	m := focusModel(t)
	m.setTaskFocus(true)
	if !m.panel.taskSelector {
		t.Fatal("setup: selector not focused")
	}

	m.applySnapshot(Snapshot{Cwd: "/repo"})

	assertFocusInvariant(t, m, "after applySnapshot emptied the list")
	if m.panel.taskSelector {
		t.Fatal("taskSelector still true after the snapshot emptied the list")
	}
	typeText(m, "x")
	if got := m.editor.Value(); got != "x" {
		t.Fatalf("editor value = %q, want %q", got, "x")
	}
}

// applyMetadata 同样要收敛焦点
func TestApplyMetadataClearsSelectorFocus(t *testing.T) {
	m := focusModel(t)
	m.setTaskFocus(true)

	m.applyMetadata(Snapshot{Cwd: "/repo"})

	assertFocusInvariant(t, m, "after applyMetadata emptied the list")
	if m.panel.taskSelector {
		t.Fatal("taskSelector still true after applyMetadata emptied the list")
	}
}
