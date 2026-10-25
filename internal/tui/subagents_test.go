package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestSubagentRowsAndViewSwitch(t *testing.T) {
	started := time.Now().Add(-2 * time.Second)
	var requests []SubagentRequest
	m := New(Snapshot{
		Cwd: "/repo",
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
			ID: "agent-1", Type: "agent", AgentType: "Explore", Status: "running",
			Description: "inspect tree", StartTime: started,
			Messages: []Message{{ID: "child-1", Role: "assistant", Text: "child output"}},
		}}},
	}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "main") || !strings.Contains(view, "Explore") || !strings.Contains(view, "inspect tree") {
		t.Fatalf("task footer missing: %q", view)
	}
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if m.viewingTaskID != "agent-1" || !strings.Contains(ansi.Strip(m.View()), "child output") {
		t.Fatalf("subagent view not entered: %q", ansi.Strip(m.View()))
	}
	if len(requests) != 1 || requests[0].Kind != "enter" || requests[0].TaskID != "agent-1" {
		t.Fatalf("enter request = %#v", requests)
	}
	press(m, tea.KeyEsc)
	if m.viewingTaskID != "" || len(requests) != 2 || requests[1].Kind != "exit" {
		t.Fatalf("subagent view not exited: view=%q requests=%#v", m.viewingTaskID, requests)
	}
}

func TestSubagentEscapeWinsOverJumpToBottom(t *testing.T) {
	var requests []SubagentRequest
	childMessages := make([]Message, 40)
	for i := range childMessages {
		childMessages[i] = Message{ID: fmt.Sprintf("child-%d", i), Role: "assistant", Text: "child output"}
	}
	m := New(Snapshot{
		Cwd: "/repo",
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
			ID: "agent-1", AgentType: "Explore", Status: "running", Description: "inspect",
			Messages: childMessages,
		}}},
	}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.enterSubagentView("agent-1")
	m.viewport.GotoTop()
	if !m.jumpPillVisible() {
		t.Fatal("subagent transcript should expose jump-to-bottom while scrolled")
	}
	press(m, tea.KeyEsc)
	if m.viewingTaskID != "" || len(requests) != 1 || requests[0].Kind != "exit" {
		t.Fatalf("escape did not leave subagent view: viewing=%q requests=%#v", m.viewingTaskID, requests)
	}
}

func TestRunningSubagentSpinnerAdvancesWithoutMainOperation(t *testing.T) {
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Explore", Status: "running", Description: "inspect",
	}}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	before := m.spinner.View()
	m.Update(spinner.TickMsg{})
	if after := m.spinner.View(); after == before {
		t.Fatalf("subagent spinner did not advance: before=%q after=%q", before, after)
	}
}

func TestSpinnerLoopStartsOnlyOnce(t *testing.T) {
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Explore", Status: "running", Description: "inspect",
	}}}}, Handlers{})
	if first := m.startSpinner(); first == nil {
		t.Fatal("first spinner loop was not started")
	}
	if second := m.startSpinner(); second != nil {
		t.Fatal("second spinner loop was started")
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Explore", Status: "completed", Description: "inspect",
	}}})
	m.Update(spinner.TickMsg{})
	if m.spinnerRunning {
		t.Fatal("spinner loop remained active after the last subagent completed")
	}
}

func TestRunningSubagentSpinnerDoesNotRefreshStaticTranscript(t *testing.T) {
	m := New(Snapshot{
		Messages:  []Message{{ID: "main", Role: "assistant", Text: "static response"}},
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "agent-1", Status: "running"}}},
	}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.viewport.SetContent("sentinel")
	m.Update(spinner.TickMsg{})
	if got := strings.TrimSpace(m.viewport.View()); got != "sentinel" {
		t.Fatalf("static transcript was refreshed by child spinner: %q", got)
	}
}
func TestSubagentTaskSuffixIsRightAligned(t *testing.T) {
	started := time.Unix(100, 0)
	ended := started.Add(2 * time.Second)
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Explore", Status: "completed", Description: strings.Repeat("long description ", 10),
		StartTime: started, EndTime: &ended, TotalTokens: 1234, PendingCount: 2,
	}}}}, Handlers{})
	m.clock = func() time.Time { return ended }
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.Contains(line, "Explore") && strings.Contains(line, "1.2k") {
			if !strings.HasSuffix(line, "2s  1.2k  ·2") {
				t.Fatalf("task suffix is not right aligned: %q", line)
			}
			if ansi.StringWidth(line) != 80 {
				t.Fatalf("task row width = %d, want 80: %q", ansi.StringWidth(line), line)
			}
			return
		}
	}
	t.Fatal("task row not rendered")
}

func TestMainRowOnlyAppearsWhenTasksExist(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo"}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	if strings.Contains(ansi.Strip(m.View()), "main") {
		t.Fatalf("empty task projection rendered main row: %q", ansi.Strip(m.View()))
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "plan", Status: "running", Description: "plan",
	}}})
	if !strings.Contains(ansi.Strip(m.View()), "main") || !strings.Contains(ansi.Strip(m.View()), "plan") {
		t.Fatalf("task projection omitted main or typed row: %q", ansi.Strip(m.View()))
	}
}

func TestSubagentViewSubmitsQueueAndContinuation(t *testing.T) {
	var requests []SubagentRequest
	base := Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Work", Status: "running", Description: "edit code",
	}}}}
	m := New(base, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.selectedTaskID = "agent-1"
	m.enterSubagentView("agent-1")
	typeText(m, "continue this")
	press(m, tea.KeyEnter)
	if len(requests) != 1 || requests[0].Kind != "queue" || requests[0].Message == nil {
		t.Fatalf("queue request = %#v", requests)
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Work", Status: "completed", Description: "edit code",
	}}})
	typeText(m, "resume this")
	press(m, tea.KeyEnter)
	if len(requests) != 2 || requests[1].Kind != "continue" || requests[1].Message == nil {
		t.Fatalf("continuation request = %#v", requests)
	}
	if _, ok := requests[1].Message.Content[0].(llm.TextContent); !ok {
		t.Fatalf("continuation message = %#v", requests[1].Message)
	}
}

func TestSubagentViewRestoresRejectedInput(t *testing.T) {
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Work", Status: "running", Description: "edit code",
	}}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		return func() tea.Msg {
			return SubagentSnapshotMsg{Err: errors.New("task finished before queue"), RestoreMessage: request.Message}
		}
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.enterSubagentView("agent-1")
	typeText(m, "recover this input")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("child request command is nil")
	}
	if message := cmd(); message == nil {
		t.Fatal("child request returned no result")
	} else {
		m.Update(message)
	}
	if got := m.editor.Value(); got != "recover this input" {
		t.Fatalf("rejected child input = %q, want restored draft", got)
	}
}

func TestSubagentRowsUseSelectionDots(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "agent-1", AgentType: "Explore", Status: "running", Description: "first"},
		{ID: "agent-2", AgentType: "Work", Status: "running", Description: "second"},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	// 1.未选中:无箭头,只有空心圆
	if rows := ansi.Strip(strings.Join(m.taskRows(), "\n")); !strings.Contains(rows, "  ○ main") || strings.Contains(rows, "❯") || strings.Contains(rows, "●") {
		t.Fatalf("initial task markers = %q", rows)
	}
	// 2.选中 child:箭头 + 实心圆,main 回到空心
	press(m, tea.KeyDown)
	if rows := ansi.Strip(strings.Join(m.taskRows(), "\n")); !strings.Contains(rows, "  ○ main") || !strings.Contains(rows, "❯ ● Explore") || !strings.Contains(rows, "  ○ Work") {
		t.Fatalf("selected task markers = %q", rows)
	}
	// 3.选中 main:箭头移到 main
	press(m, tea.KeyUp)
	if rows := ansi.Strip(strings.Join(m.taskRows(), "\n")); !strings.Contains(rows, "❯ ● main") || !strings.Contains(rows, "  ○ Explore") {
		t.Fatalf("main selection markers = %q", rows)
	}
	// 4.再次选中 child
	press(m, tea.KeyDown)
	if rows := ansi.Strip(strings.Join(m.taskRows(), "\n")); !strings.Contains(rows, "  ○ main") || !strings.Contains(rows, "❯ ● Explore") {
		t.Fatalf("child reselection markers = %q", rows)
	}
	expected := lipgloss.NewStyle().Foreground(lipgloss.Color(textColor)).Render("❯ ●")
	if !strings.Contains(m.taskRows()[1], expected) {
		t.Fatalf("selected marker color = %q, want white", m.taskRows()[1])
	}
}

func TestKilledTaskKeepsHollowRedMarker(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "agent-1", AgentType: "Work", Status: "killed", Description: "stopped"},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	// 1.未选中时是暗色空心圆
	if row := m.taskRows()[1]; strings.Contains(row, "●") {
		t.Fatalf("killed task rendered a filled dot while unselected: %q", row)
	}
	press(m, tea.KeyDown)
	// 2.选中后仍是空心圆,只用右箭头表示选中
	row := m.taskRows()[1]
	if !strings.Contains(ansi.Strip(row), "❯ ○") || strings.Contains(ansi.Strip(row), "●") {
		t.Fatalf("killed task selection = %q", ansi.Strip(row))
	}
	// 3.圆点是红色
	red := lipgloss.NewStyle().Foreground(lipgloss.Color(redColor)).Render("❯ ○")
	if !strings.Contains(row, red) {
		t.Fatalf("killed task marker = %q, want red hollow", row)
	}
}
func TestKilledTaskKeepsRowPosition(t *testing.T) {
	first := time.Unix(100, 0)
	second := first.Add(time.Second)
	third := second.Add(time.Second)
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "agent-1", AgentType: "Explore", Status: "completed", Description: "first", StartTime: first},
		{ID: "agent-2", AgentType: "Work", Status: "running", Description: "second", StartTime: second},
		{ID: "agent-3", AgentType: "Plan", Status: "running", Description: "third", StartTime: third},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	before := m.taskRows()
	// 1.终止中间那个任务后,顺序必须保持不变
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "agent-1", AgentType: "Explore", Status: "completed", Description: "first", StartTime: first},
		{ID: "agent-2", AgentType: "Work", Status: "killed", Description: "second", StartTime: second},
		{ID: "agent-3", AgentType: "Plan", Status: "running", Description: "third", StartTime: third},
	}})
	after := m.taskRows()
	if len(before) != len(after) {
		t.Fatalf("row count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		beforeLabel := ansi.Strip(before[i])
		afterLabel := ansi.Strip(after[i])
		beforeName := strings.TrimSpace(strings.SplitN(beforeLabel, "  ", 2)[0])
		afterName := strings.TrimSpace(strings.SplitN(afterLabel, "  ", 2)[0])
		if strings.TrimSpace(beforeName) != strings.TrimSpace(afterName) {
			t.Fatalf("row %d moved after kill: %q -> %q", i, beforeLabel, afterLabel)
		}
	}
	// 2.被终止的任务仍留在原来的第二行,且是红色空心圆
	if row := ansi.Strip(after[2]); !strings.Contains(row, "Work") || !strings.Contains(row, "○") || strings.Contains(row, "●") {
		t.Fatalf("killed task row = %q", row)
	}
}

func TestSubagentRefreshLoopIsSingleOwner(t *testing.T) {
	running := []SubagentTaskView{{ID: "agent-1", AgentType: "Explore", Status: "running", Description: "inspect"}}
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: running}}, Handlers{SubagentSnapshot: func() tea.Cmd { return nil }})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	// 1.首个 snapshot 建立唯一 tick 链
	if _, cmd := m.Update(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{Revision: 1, Tasks: running}}); cmd == nil {
		t.Fatal("first snapshot did not start the refresh chain")
	}
	// 2.推送来的 snapshot 不能再开一条链，否则 tick 数每秒翻倍
	if _, cmd := m.Update(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{Revision: 2, Tasks: running}}); cmd != nil {
		t.Fatal("pushed snapshot started a second refresh chain")
	}
	// 3.tick 只续接自身，一条链仍对应一个 tick
	if _, cmd := m.Update(subagentRefreshMsg{}); cmd == nil {
		t.Fatal("refresh tick did not renew the chain")
	}
	if _, cmd := m.Update(subagentRefreshMsg{}); cmd == nil {
		t.Fatal("refresh tick did not renew the chain after renewal")
	}
	// 4.最后一个任务结束后整条链必须停止
	done := []SubagentTaskView{{ID: "agent-1", AgentType: "Explore", Status: "completed", Description: "inspect"}}
	if _, cmd := m.Update(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{Revision: 3, Tasks: done}}); cmd != nil {
		t.Fatal("refresh chain survived the last running task")
	}
	if m.subagentRefreshRunning {
		t.Fatal("refresh loop flag was left set after completion")
	}
}

func TestSubagentViewHandlesRawEscape(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Explore", Status: "running", Description: "inspect",
	}}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.enterSubagentView("agent-1")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\x1b'}})
	if m.viewingTaskID != "" || len(requests) != 1 || requests[0].Kind != "exit" {
		t.Fatalf("raw Escape did not exit child view: viewing=%q requests=%#v", m.viewingTaskID, requests)
	}
}

func TestSubagentSnapshotKeepsChildTranscriptCache(t *testing.T) {
	child := SubagentTaskView{ID: "agent-1", AgentType: "Work", Status: "running", Description: "inspect", Messages: []Message{{ID: "child-1", Role: "assistant", Text: "child output"}}}
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{child}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.enterSubagentView("agent-1")
	if len(m.cache) == 0 {
		t.Fatal("child transcript was not cached")
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{child}})
	if len(m.cache) == 0 {
		t.Fatal("unchanged child snapshot cleared transcript cache")
	}
}

func TestSubagentViewIgnoresMainStreamRendering(t *testing.T) {
	m := New(Snapshot{
		Cwd: "/repo",
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
			ID: "agent-1", AgentType: "Work", Status: "running", Description: "child",
			Messages: []Message{{ID: "child-1", Role: "assistant", Text: "child output"}},
		}}},
	}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("agent-1")
	m.viewport.SetContent("child sentinel")
	// 1.主 Agent 的流式消息只进 main 缓存
	m.Update(MessageMsg{ID: "main-live", Role: "assistant", Text: "main stream"})
	// 2.child viewport 不得被这次消息重建
	if got := strings.TrimSpace(m.viewport.View()); got != "child sentinel" {
		t.Fatalf("main stream rebuilt the child viewport: %q", got)
	}
	if m.flushPending {
		t.Fatal("main stream scheduled a transcript flush inside the child view")
	}
	if len(m.mainMessages) == 0 || m.mainMessages[len(m.mainMessages)-1].ID != "main-live" {
		t.Fatalf("main stream was not cached for later: %+v", m.mainMessages)
	}
	// 3.退出后主 transcript 恢复并包含该消息
	press(m, tea.KeyEsc)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "main stream") {
		t.Fatalf("main stream missing after leaving the child view: %q", view)
	}
}

func TestSubagentViewKeepsMainMessagesAndNavigates(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{
		Cwd:      "/repo",
		Messages: []Message{{ID: "main-old", Role: "assistant", Text: "main history"}},
		Steering: []string{"main pending"},
		Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
			{ID: "agent-1", AgentType: "Explore", Status: "running", Description: "first", Messages: []Message{{ID: "child-1", Role: "assistant", Text: "child one"}}},
			{ID: "agent-2", AgentType: "Work", Status: "completed", Description: "second", Messages: []Message{{ID: "child-2", Role: "assistant", Text: "child two"}}},
		}},
	}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("agent-1")
	if childView := ansi.Strip(m.View()); strings.Contains(childView, "main pending") {
		t.Fatalf("main steering leaked into child view: %q", childView)
	}
	m.Update(MessageMsg{ID: "main-live", Role: "assistant", Text: "main live response"})
	if strings.Contains(ansi.Strip(m.View()), "main live response") {
		t.Fatalf("main stream leaked into child view: %q", ansi.Strip(m.View()))
	}
	press(m, tea.KeyDown)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "○ Explore") || !strings.Contains(view, "● Work") {
		t.Fatalf("child selection dots = %q", view)
	}
	press(m, tea.KeyEnter)
	if m.viewingTaskID != "agent-2" || len(requests) != 1 || requests[0].Kind != "enter" || requests[0].TaskID != "agent-2" {
		t.Fatalf("child navigation = viewing %q requests %#v", m.viewingTaskID, requests)
	}
	press(m, tea.KeyEsc)
	if m.viewingTaskID != "" || len(requests) != 2 || requests[1].Kind != "exit" {
		t.Fatalf("child exit = viewing %q requests %#v", m.viewingTaskID, requests)
	}
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "main live response") || !strings.Contains(view, "main pending") || strings.Contains(view, "child two") {
		t.Fatalf("main transcript after exit = %q", view)
	}
}

// 详情页里输入框有内容时 x 是普通字符,不能触发 stop/evict
func TestSubagentViewTypesXInsteadOfStoppingWhenEditorHasText(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Work", Status: "running", Description: "edit code",
	}}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.enterSubagentView("agent-1")
	typeText(m, "fi")
	typeText(m, "x")
	if got := m.editor.Value(); got != "fix" {
		t.Fatalf("editor value = %q, want %q", got, "fix")
	}
	if len(requests) != 0 {
		t.Fatalf("x triggered a subagent request: %#v", requests)
	}
	// 1.清空输入框后 x 恢复 stop 语义
	m.editor.Reset()
	typeText(m, "x")
	if len(requests) != 1 || requests[0].Kind != "stop" || requests[0].TaskID != "agent-1" {
		t.Fatalf("stop request = %#v", requests)
	}
	if got := m.editor.Value(); got != "" {
		t.Fatalf("stop key leaked into the editor: %q", got)
	}
}

// 主视图里输入框为空时 x 仍是 stop/evict;有内容时是普通字符
func TestMainViewXRequiresEmptyEditor(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "agent-1", AgentType: "Work", Status: "running", Description: "edit code",
	}}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.selectedTaskID = "agent-1"
	typeText(m, "a")
	typeText(m, "x")
	if got := m.editor.Value(); got != "ax" {
		t.Fatalf("editor value = %q, want %q", got, "ax")
	}
	if len(requests) != 0 {
		t.Fatalf("x triggered a subagent request while typing: %#v", requests)
	}
}
