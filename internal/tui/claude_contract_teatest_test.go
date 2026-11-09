package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
)

// async_launched 的工具结果与任务面板必须同时可见,且只向用户展示可行动的后台反馈
func TestClaudeContractLaunchFeedbackRendersWithTaskRow(t *testing.T) {
	model := New(Snapshot{
		Cwd: "/repo",
		Messages: []Message{{
			ID:   "assistant-1",
			Role: "assistant",
			ToolGroup: &ToolGroupView{Calls: []ToolCallView{{
				ID: "call-1", Name: "agent",
				Arguments:    `{"description":"Run sleep 120","subagent_type":"explore"}`,
				Result:       "Started subagent agent-123",
				Status:       ToolSuccess,
				ArgsComplete: true,
			}}},
		}},
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
			ID: "agent-123", AgentType: "explore", Status: "running", Description: "Run sleep 120",
		}}},
	}, Handlers{})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 24))
	tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*Model)
	view := ansi.Strip(final.View())
	if !strings.Contains(view, "Running in the background; you'll be notified when it finishes") {
		t.Fatalf("launch feedback missing:\n%s", view)
	}
	if !strings.Contains(view, "explore  Run sleep 120") {
		t.Fatalf("launched task row missing:\n%s", view)
	}
	if strings.Contains(view, "agent-123") {
		t.Fatalf("internal task ID leaked into launch UI:\n%s", view)
	}
}

// child view 的 Esc 停止当前 subagent;选中 main 后 Enter 才关闭详情
func TestClaudeContractStoppedSubagentRequiresMainEnter(t *testing.T) {
	model := New(Snapshot{
		Cwd:      "/repo",
		Messages: []Message{{ID: "main", Role: "assistant", Text: "main transcript"}},
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
			ID: "agent-1", AgentType: "Explore", Status: "running", Description: "running task",
			Messages: []Message{{ID: "child", Role: "assistant", Text: "child transcript"}},
		}}},
	}, Handlers{})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.KeyMsg{Type: tea.KeyLeft})
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	tm.Send(tea.KeyMsg{Type: tea.KeyLeft})
	tm.Send(tea.KeyMsg{Type: tea.KeyUp})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlD})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*Model)
	view := ansi.Strip(final.View())
	if !strings.Contains(view, "main transcript") || strings.Contains(view, "child transcript") {
		t.Fatalf("main Enter did not close stopped subagent view:\n%s", view)
	}
}

// 后台任务终态更新不能覆盖主 agent 草稿,任务面板也必须保持固定终端高度
func TestClaudeContractKilledUpdatePreservesMainDraftAndFrame(t *testing.T) {
	model := New(Snapshot{
		Cwd: "/repo",
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
			ID: "agent-1", AgentType: "Explore", Status: "running", Description: "long inspection",
		}}},
	}, Handlers{})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(80, 12))
	tm.Type("keep this draft")
	tm.Send(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{
		Revision: 2,
		Tasks: []SubagentTaskView{{
			ID: "agent-1", AgentType: "Explore", Status: "killed", Description: "long inspection",
		}},
	}})
	tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*Model)
	if got := final.editor.Value(); got != "keep this draft" {
		t.Fatalf("draft after killed update = %q, want preserved draft", got)
	}
	view := ansi.Strip(final.View())
	if !strings.Contains(view, "Stopped: long inspection") {
		t.Fatalf("killed task row missing stopped label:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) != 12 {
		t.Fatalf("rendered %d lines, want terminal height 12:\n%s", len(lines), view)
	}
}

// 子视图切换期间主 agent 的消息不能泄漏到 child transcript,退出后必须恢复主 transcript
func TestClaudeContractChildNavigationKeepsMainTranscriptIsolated(t *testing.T) {
	model := New(Snapshot{
		Cwd:      "/repo",
		Messages: []Message{{ID: "main-old", Role: "assistant", Text: "main history"}},
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
			ID: "agent-1", AgentType: "Explore", Status: "running", Description: "child work",
			Messages: []Message{{ID: "child", Role: "assistant", Text: "child output"}},
		}}},
	}, Handlers{})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.KeyMsg{Type: tea.KeyLeft})
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(MessageMsg{ID: "main-live", Role: "assistant", Text: "main live"})
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	tm.Send(tea.KeyMsg{Type: tea.KeyLeft})
	tm.Send(tea.KeyMsg{Type: tea.KeyUp})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlD})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*Model)
	view := ansi.Strip(final.View())
	if !strings.Contains(view, "main history") || !strings.Contains(view, "main live") || strings.Contains(view, "child output") {
		t.Fatalf("main/child transcript isolation failed:\n%s", view)
	}
}
