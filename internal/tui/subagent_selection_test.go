package tui

import (
	"testing"
	"time"
)

// 选中任务被驱逐后,光标先落到邻居;没有邻居才回 main 行
func TestSelectionFallsBackToNeighborWhenTaskEvicted(t *testing.T) {
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
	// 被选中的任务消失,剩下的那个成为邻居
	selected := m.panel.selectedTaskID
	var remaining []SubagentTaskView
	for _, task := range tasks {
		if task.ID != selected {
			remaining = append(remaining, task)
		}
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: remaining})
	if m.panel.mainSelected || m.panel.selectedTaskID != remaining[0].ID {
		t.Fatalf("selection did not fall back to the neighbor: main=%v id=%q, want %q",
			m.panel.mainSelected, m.panel.selectedTaskID, remaining[0].ID)
	}
	// 最后一个也消失时才回 main 行
	m.applySubagentSnapshot(SubagentSnapshot{})
	if !m.panel.mainSelected || m.panel.selectedTaskID != "" {
		t.Fatalf("selection did not fall back to main: main=%v id=%q", m.panel.mainSelected, m.panel.selectedTaskID)
	}
}
