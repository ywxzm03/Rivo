package tui

import (
	"testing"
	"time"
)

// 选中任务被驱逐后,列表必须回到 main 行,不能停在"全都没选中"
func TestSelectionFallsBackToMainWhenTaskEvicted(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one", StartTime: time.Now()},
		{ID: "a2", AgentType: "explore", Status: "running", Description: "two", StartTime: time.Now()},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(teaWindow(100, 16))
	m.setTaskFocus(true)
	m.selectSubagent(1)
	if m.panel.mainSelected || m.panel.selectedTaskID == "" {
		t.Fatalf("selection did not move to a task: main=%v id=%q", m.panel.mainSelected, m.panel.selectedTaskID)
	}
	// 被选中的任务消失
	selected := m.panel.selectedTaskID
	var remaining []SubagentTaskView
	for _, task := range tasks {
		if task.ID != selected {
			remaining = append(remaining, task)
		}
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: remaining})
	if !m.panel.mainSelected || m.panel.selectedTaskID != "" {
		t.Fatalf("selection did not fall back to main: main=%v id=%q", m.panel.mainSelected, m.panel.selectedTaskID)
	}
}
