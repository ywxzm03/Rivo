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
	if m.viewingTaskID != "agent-1" || len(requests) != 2 || requests[1].Kind != "stop" || requests[1].TaskID != "agent-1" {
		t.Fatalf("Esc did not stop child: view=%q requests=%#v", m.viewingTaskID, requests)
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "agent-1", Type: "agent", AgentType: "Explore", Status: "killed", Description: "inspect tree", Messages: []Message{{ID: "child-1", Role: "assistant", Text: "child output"}}}}})
	press(m, tea.KeyUp)
	press(m, tea.KeyEnter)
	if m.viewingTaskID != "" || len(requests) != 3 || requests[2].Kind != "exit" {
		t.Fatalf("main Enter did not exit child: view=%q requests=%#v", m.viewingTaskID, requests)
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
	// child view 的 Esc 优先停止当前任务,不能被 jump-to-bottom 吞掉
	press(m, tea.KeyEsc)
	if m.viewingTaskID != "agent-1" || len(requests) != 1 || requests[0].Kind != "stop" {
		t.Fatalf("Escape did not stop child: viewing=%q requests=%#v", m.viewingTaskID, requests)
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

// 圆点表示正在查看的作用域,❯ 表示选中行,两者互相独立
func TestSubagentRowsUseSelectionDots(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "agent-1", AgentType: "Explore", Status: "running", Description: "first"},
		{ID: "agent-2", AgentType: "Work", Status: "running", Description: "second"},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	// 1.输入框聚焦:main 是当前作用域,实心;没有箭头
	rows := ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if !strings.Contains(rows, "  ⏺ main") || strings.Contains(rows, "❯") {
		t.Fatalf("initial task markers = %q", rows)
	}
	// 2.进入列表:箭头落在 main,main 仍是实心
	press(m, tea.KeyUp)
	rows = ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if !strings.Contains(rows, "❯ ⏺ main") {
		t.Fatalf("list focus markers = %q", rows)
	}
	// 3.下移到任务:main 变空心,选中项变实心
	press(m, tea.KeyDown)
	rows = ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if !strings.Contains(rows, "  ◯ main") || !strings.Contains(rows, "❯ ⏺ Explore") {
		t.Fatalf("selected task markers = %q", rows)
	}
}

// 被终止的任务用红色圆点:未选中空心,选中实心
func TestKilledTaskKeepsHollowRedMarker(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "agent-1", AgentType: "Work", Status: "killed", Description: "stopped"},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	// 1.未选中是红色空心圆
	if row := m.taskRows()[1]; strings.Contains(row, "●") {
		t.Fatalf("killed task rendered a filled dot while unselected: %q", row)
	}
	// 2.进入列表但停在 main 行时,被终止任务仍未选中
	press(m, tea.KeyUp)
	row := m.taskRows()[1]
	if !strings.Contains(ansi.Strip(row), "  ◯") || strings.Contains(ansi.Strip(row), "●") {
		t.Fatalf("killed task unselected = %q", ansi.Strip(row))
	}
	red := lipgloss.NewStyle().Foreground(lipgloss.Color(redColor)).Render("◯")
	if !strings.Contains(row, red) {
		t.Fatalf("killed task marker = %q, want red hollow", row)
	}
}

// 被终止的任务被选中时圆点填充,颜色仍是红色
func TestKilledTaskFillsDotWhenSelected(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "agent-1", AgentType: "Work", Status: "killed", Description: "stopped"},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	press(m, tea.KeyUp)
	m.selectedTaskID = "agent-1"
	m.mainSelected = false
	row := m.taskRows()[1]
	if !strings.Contains(ansi.Strip(row), "❯ ●") {
		t.Fatalf("selected killed task = %q, want filled dot", ansi.Strip(row))
	}
	red := lipgloss.NewStyle().Foreground(lipgloss.Color(redColor)).Render("●")
	if !strings.Contains(row, red) {
		t.Fatalf("selected killed dot = %q, want red", row)
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
	// 2.被终止的任务仍在第二行,是红色空心圆
	if row := ansi.Strip(after[2]); !strings.Contains(row, "Work") || !strings.Contains(row, "◯") || strings.Contains(row, "●") {
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
	// 4.终态任务仍留在面板时链必须保持存活(已有链时不再开新链)
	done := []SubagentTaskView{{ID: "agent-1", AgentType: "Explore", Status: "completed", Description: "inspect"}}
	m.Update(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{Revision: 3, Tasks: done}})
	if !m.needsSubagentRefresh() {
		t.Fatal("terminal task should keep the refresh chain alive until eviction")
	}
	// 5.任务被驱逐后快照为空,链必须停止
	m.Update(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{Revision: 4}})
	if m.needsSubagentRefresh() {
		t.Fatal("refresh should stop once every task is evicted")
	}
	if _, cmd := m.Update(subagentRefreshMsg{}); cmd != nil {
		t.Fatal("refresh chain survived eviction of the last task")
	}
	if m.subagentRefreshRunning {
		t.Fatal("refresh loop flag was left set after eviction")
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
	if m.viewingTaskID != "agent-1" || len(requests) != 1 || requests[0].Kind != "stop" {
		t.Fatalf("raw Escape did not stop child: viewing=%q requests=%#v", m.viewingTaskID, requests)
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
	// 3.选中 main 后 Enter 退出,主 transcript 恢复并包含该消息
	press(m, tea.KeyUp)
	press(m, tea.KeyEnter)
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
	// 1.进入视图后输入框保持焦点,当前 child 用实心圆表示但没有箭头
	rows := ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if !strings.Contains(rows, "  ⏺ Explore") || strings.Contains(rows, "❯") {
		t.Fatalf("entered child selection = %q", rows)
	}
	// 2.Down 首次进入列表并从当前 child 移到第二个任务
	press(m, tea.KeyDown)
	rows = ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if !strings.Contains(rows, "◯ Explore") || !strings.Contains(rows, "❯ ⏺ Work") {
		t.Fatalf("second child selection = %q", rows)
	}
	press(m, tea.KeyEnter)
	if m.viewingTaskID != "agent-2" || len(requests) != 1 || requests[0].Kind != "enter" || requests[0].TaskID != "agent-2" {
		t.Fatalf("child navigation = viewing %q requests %#v", m.viewingTaskID, requests)
	}
	// 先回到 agent-1,再回到 main
	press(m, tea.KeyUp)
	press(m, tea.KeyUp)
	press(m, tea.KeyEnter)
	if m.viewingTaskID != "" || len(requests) != 2 || requests[1].Kind != "exit" {
		t.Fatalf("main Enter did not exit child = viewing %q requests %#v", m.viewingTaskID, requests)
	}
	mainView := ansi.Strip(m.View())
	if !strings.Contains(mainView, "main live response") || !strings.Contains(mainView, "main pending") || strings.Contains(mainView, "child two") {
		t.Fatalf("main transcript after exit = %q", mainView)
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
	// 1.焦点移到任务列表后 x 才是 stop
	m.editor.Reset()
	m.setTaskFocus(true)
	m.selectedTaskID = "agent-1"
	m.mainSelected = false
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
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

// 任务面板出现后,输入框必须仍在屏幕内:viewport 要给任务行让出高度
func TestTaskRowsDoNotSwallowEditor(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "agent-1", AgentType: "work", Status: "running", Description: "first"},
		{ID: "agent-2", AgentType: "work", Status: "running", Description: "second"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	view := ansi.Strip(m.View())
	lines := strings.Split(view, "\n")
	if len(lines) != 20 {
		t.Fatalf("rendered %d lines, want exactly the terminal height 20", len(lines))
	}
	// 输入框提示符与任务行必须同时可见
	if !strings.Contains(view, "❯") {
		t.Fatalf("editor prompt was swallowed by the task panel:\n%s", view)
	}
	if !strings.Contains(view, "main") || !strings.Contains(view, "first") {
		t.Fatalf("task rows missing:\n%s", view)
	}
}

// 任务行数量增加时输入框仍要保留
func TestManyTaskRowsKeepEditorVisible(t *testing.T) {
	var tasks []SubagentTaskView
	for i := 0; i < 8; i++ {
		tasks = append(tasks, SubagentTaskView{ID: fmt.Sprintf("agent-%d", i), AgentType: "work", Status: "running", Description: fmt.Sprintf("task %d", i)})
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "❯") {
		t.Fatalf("editor prompt missing with 8 task rows:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) != 24 {
		t.Fatalf("rendered %d lines, want 24", len(lines))
	}
}

// 运行中且终端较矮时,输入框必须仍然可见(总行数溢出只能裁 transcript 顶部)
func TestEditorVisibleWhileBusyOnShortTerminal(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "work", Status: "running", Description: "two"},
		{ID: "a3", AgentType: "work", Status: "running", Description: "three"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	m.busy, m.status = true, "Working"
	m.state.Messages = []Message{{ID: "x", Role: "assistant", Text: strings.Repeat("content line\n", 40)}}
	m.rebuildMessageIndex()
	m.layout()
	m.refreshTranscript()

	view := ansi.Strip(m.View())
	if lines := strings.Split(view, "\n"); len(lines) != 12 {
		t.Fatalf("rendered %d lines, want exactly 12", len(lines))
	}
	if !strings.Contains(view, "❯") {
		t.Fatalf("editor prompt was swallowed while busy:\n%s", view)
	}
	if !strings.Contains(view, "Working") {
		t.Fatalf("running status border missing:\n%s", view)
	}
	if !strings.Contains(view, "three") {
		t.Fatalf("task rows missing:\n%s", view)
	}
}

// 进入已终态的子任务视图后,下一份快照必须被接受,不能因 revision 判定被丢弃
func TestEnterViewAcceptsNextSnapshot(t *testing.T) {
	done := SubagentTaskView{ID: "a1", AgentType: "work", Status: "completed", Description: "done"}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{done}, Revision: 7}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("a1")
	if m.state.Subagents.Revision != 0 {
		t.Fatalf("revision baseline = %d, want 0 so the next snapshot is accepted", m.state.Subagents.Revision)
	}
	loaded := done
	loaded.Messages = []Message{{ID: "a1/entry/0", Role: "assistant", Text: "child transcript"}}
	loaded.Revision = 1
	m.Update(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{Tasks: []SubagentTaskView{loaded}, ViewingTaskID: "a1", Revision: 8}})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "child transcript") {
		t.Fatalf("child transcript not rendered after the first snapshot:\n%s", view)
	}
}

// 重新进入已看过的任务时,必须重新投影 transcript,不能沿用空缓存
func TestReenterViewReloadsTranscript(t *testing.T) {
	loaded := SubagentTaskView{
		ID: "a1", AgentType: "work", Status: "completed", Description: "done", Revision: 9,
		Messages: []Message{{ID: "a1/entry/0", Role: "assistant", Text: "child transcript"}},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{loaded}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.enterSubagentView("a1")
	if view := ansi.Strip(m.View()); !strings.Contains(view, "child transcript") {
		t.Fatalf("first enter did not render the transcript:\n%s", view)
	}
	m.exitSubagentView()
	m.enterSubagentView("a1")
	// 快照内容与 revision 都没变,但重新进入必须重新投影
	m.Update(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{Tasks: []SubagentTaskView{loaded}, ViewingTaskID: "a1", Revision: 10}})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "child transcript") {
		t.Fatalf("re-enter lost the transcript:\n%s", view)
	}
}

// 输入框非空时,光标在边界按上下键仍能切到任务选择
func TestTaskSelectionWorksWithNonEmptyEditor(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "work", Status: "running", Description: "two"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.editor.SetValue("some draft text")

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !m.taskSelector || m.selectedTaskID == "" {
		t.Fatalf("Down with non-empty editor did not select a task: selector=%v id=%q", m.taskSelector, m.selectedTaskID)
	}
	if m.editor.Value() != "some draft text" {
		t.Fatalf("editor content changed: %q", m.editor.Value())
	}
}

// 多行输入时,光标不在边界仍应移动光标而不是切任务
func TestTaskSelectionRespectsCursorPosition(t *testing.T) {
	tasks := []SubagentTaskView{{ID: "a1", AgentType: "work", Status: "running", Description: "one"}}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.editor.SetValue("line one\nline two\nline three")
	m.editor.CursorStart()
	// 三行文本里光标在中间行时,Down 必须移动光标而不是切任务
	for m.editor.Line() > 1 {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	before := m.editor.Line()
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.editor.Line() == before {
		t.Fatalf("Down in the middle of a multi-line draft did not move the cursor (line=%d)", before)
	}
	if m.taskSelector {
		t.Fatal("task selector opened while the cursor was still moving")
	}
}

// 子任务视图的撤回键发的是 dequeue 请求,而不是操作主 agent 队列
func TestSubagentViewDequeuesOwnQueue(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "a1", AgentType: "work", Status: "running", Description: "inspect",
	}}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("a1")
	m.busy = true

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\x1b'}, Alt: true})
	_ = m.dequeueSubagentMessages(true)
	if len(requests) == 0 {
		t.Fatal("follow-up dequeue did not reach the subagent")
	}
	last := requests[len(requests)-1]
	if last.Kind != "dequeue" || last.TaskID != "a1" || !last.FollowUp {
		t.Fatalf("request = %+v, want dequeue follow-up for a1", last)
	}
}

// 撤回的子任务消息按顺序回到编辑器
func TestSubagentMessagesRestoreToEditor(t *testing.T) {
	m := New(Snapshot{}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.restoreSubagentMessages([]llm.UserMessage{
		{Content: []llm.Content{llm.TextContent{Text: "first"}}},
		{Content: []llm.Content{llm.TextContent{Text: "second"}}},
	})
	if got := m.editor.Value(); !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("editor = %q, want both restored messages", got)
	}
}

// 子任务视图必须渲染该任务自己的队列内容,而不是只显示计数
func TestSubagentViewRendersOwnQueues(t *testing.T) {
	task := SubagentTaskView{
		ID: "a1", AgentType: "work", Status: "running", Description: "inspect",
		PendingSteering: []string{"steer one"},
		PendingFollowUp: []string{"follow later"},
		PendingCount:    2,
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{task}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.enterSubagentView("a1")
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Steering: steer one") {
		t.Fatalf("steering queue not rendered:\n%s", view)
	}
	if !strings.Contains(view, "Follow-up: follow later") {
		t.Fatalf("follow-up queue not rendered:\n%s", view)
	}
}

// 主视图渲染主 agent 的队列,不受子任务视图影响
func TestMainViewRendersMainQueues(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Steering: []string{"main steer"}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Steering: main steer") {
		t.Fatalf("main steering not rendered:\n%s", view)
	}
}

// 任务列表聚焦与否都不再渲染输入框下方的提示行或空白占位
func TestTaskRowsHaveNoPromptLine(t *testing.T) {
	tasks := []SubagentTaskView{{ID: "a1", AgentType: "work", Status: "running", Description: "one"}}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	for _, focused := range []bool{false, true} {
		m.setTaskFocus(focused)
		rows := m.taskRows()
		if len(rows) != 2 || strings.Contains(strings.Join(rows, "\n"), "to select") || strings.Contains(strings.Join(rows, "\n"), "\n\n") {
			t.Fatalf("focused=%v task rows = %#v", focused, rows)
		}
	}
}

// 子任务视图的 Alt+Enter 必须进 follow-up 队列,而不是 steering
func TestSubagentViewSubmitsFollowUp(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "a1", AgentType: "work", Status: "running", Description: "inspect",
	}}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("a1")

	m.editor.SetValue("later please")
	m.submitFollowUp()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].Kind != "followup" {
		t.Fatalf("kind = %q, want followup", requests[0].Kind)
	}

	// Enter 仍然进 steering
	m.editor.SetValue("now please")
	m.submit()
	if len(requests) != 2 || requests[1].Kind != "queue" {
		t.Fatalf("enter submit = %+v, want queue", requests)
	}
}

// 队列内容为空时不能留下孤立空行
func TestPendingLinesNeverLeavesOrphanBlank(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Steering: []string{"one"}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 8})
	lines := m.pendingLines()
	if len(lines) == 0 {
		t.Fatal("pending lines empty")
	}
	nonEmpty := 0
	for _, line := range lines {
		if strings.TrimSpace(ansi.Strip(line)) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		t.Fatalf("pending lines are only blanks: %q", lines)
	}
}

// 视口被压到 0 行时,输入框上方不能再留空行,否则总行数溢出
// 高度紧张时视口仍保留 1 行,输入框不被挤掉,也不在底部留补白
func TestTightHeightKeepsEditorVisible(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "work", Status: "running", Description: "two"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 8})
	m.busy, m.status = true, "Working"
	m.layout()
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) != 8 {
		t.Fatalf("rendered %d lines, want 8", len(lines))
	}
	if m.viewport.Height < 1 {
		t.Fatalf("viewport collapsed to %d rows", m.viewport.Height)
	}
	if strings.TrimSpace(lines[len(lines)-1]) == "" {
		t.Fatalf("tail padding below the task list:\n%s", strings.Join(lines, "\n"))
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, "❯") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("editor prompt missing:\n%s", strings.Join(lines, "\n"))
	}
}

// 列表聚焦时打字把焦点交回输入框,字符继续落到文本
func TestTaskFocusReleasesOnTyping(t *testing.T) {
	tasks := []SubagentTaskView{{ID: "a1", AgentType: "work", Status: "running", Description: "one"}}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.editor.SetValue("draft")
	m.setTaskFocus(true)

	typeText(m, "abc")
	if got := m.editor.Value(); got != "draftabc" {
		t.Fatalf("editor = %q, want draftabc", got)
	}
	if m.taskSelector {
		t.Fatal("typing should return focus to the editor")
	}
}

// 任务列表聚焦时 Up/Down 只在任务间移动,不回退输入历史
func TestTaskFocusUpDownSelectsTasks(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "work", Status: "running", Description: "two"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.setTaskFocus(true)
	m.mainSelected, m.selectedTaskID = true, ""

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.mainSelected || m.selectedTaskID != "a1" {
		t.Fatalf("after down: main=%v id=%q", m.mainSelected, m.selectedTaskID)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.selectedTaskID != "a2" {
		t.Fatalf("after second down: id=%q", m.selectedTaskID)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.selectedTaskID != "a1" {
		t.Fatalf("after up: id=%q", m.selectedTaskID)
	}
	if got := m.editor.Value(); got != "" {
		t.Fatalf("editor changed while navigating tasks: %q", got)
	}
}

// Esc 从任务列表回到输入框,光标恢复
func TestTaskFocusEscReturnsToEditor(t *testing.T) {
	tasks := []SubagentTaskView{{ID: "a1", AgentType: "work", Status: "running", Description: "one"}}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.setTaskFocus(true)
	m.selectedTaskID = "a1"

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.taskSelector {
		t.Fatal("esc did not leave the task list")
	}
	// 光标回到 main 行,列表不能出现全都没有选中的状态
	if m.selectedTaskID != "" || !m.mainSelected {
		t.Fatalf("selection not returned to main: main=%v id=%q", m.mainSelected, m.selectedTaskID)
	}
	m.View()
	if row := m.cursorRow.Load(); row == 0 {
		t.Fatal("cursor not restored after returning to the editor")
	}
}

// 输入框首行的 Up 把焦点移到任务列表,且不插入字符
func TestUpFromEditorMovesFocusToTasks(t *testing.T) {
	tasks := []SubagentTaskView{{ID: "a1", AgentType: "work", Status: "running", Description: "one"}}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.editor.SetValue("hi")
	m.editor.CursorEnd()

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if !m.taskSelector {
		t.Fatal("up from the first editor line did not focus the task list")
	}
	if got := m.editor.Value(); got != "hi" {
		t.Fatalf("editor = %q, want hi", got)
	}
}

// 焦点在任务列表时输入框光标必须隐藏,避免与选中高亮同时出现
func TestCursorHiddenWhenTasksFocused(t *testing.T) {
	tasks := []SubagentTaskView{{ID: "a1", AgentType: "work", Status: "running", Description: "one"}}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.View()
	if row := m.cursorRow.Load(); row == 0 {
		t.Fatal("cursor should be visible while the editor has focus")
	}

	m.setTaskFocus(true)
	m.View()
	if row := m.cursorRow.Load(); row != 0 {
		t.Fatalf("cursor row = %d, want 0 (hidden) when the task list has focus", row)
	}
}

// 任务列表的行数预算必须等于实际渲染行数,否则 viewport 多留一行会挤掉输入框
func TestSubagentRowsMatchesRenderedRows(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "work", Status: "running", Description: "two"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	for _, focused := range []bool{false, true} {
		m.setTaskFocus(focused)
		if got, want := m.subagentRows(), len(m.taskRows()); got != want {
			t.Fatalf("focused=%v subagentRows=%d rendered=%d", focused, got, want)
		}
	}
}

// child 视图里 Esc 停止当前任务并保持详情,选中 main 后 Enter 才退出
func TestSubagentViewEscStopsAndMainEnterExits(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "long job"},
	}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("a1")
	press(m, tea.KeyEsc)
	if m.viewingTaskID != "a1" || len(requests) != 1 || requests[0].Kind != "stop" {
		t.Fatalf("Esc did not stop child in place: view=%q requests=%#v", m.viewingTaskID, requests)
	}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "a1", AgentType: "work", Status: "killed", Description: "long job"}}})
	press(m, tea.KeyUp)
	press(m, tea.KeyEnter)
	if m.viewingTaskID != "" || len(requests) != 2 || requests[1].Kind != "exit" {
		t.Fatalf("main Enter did not exit child: view=%q requests=%#v", m.viewingTaskID, requests)
	}
}

// killed 的任务被续跑后,红色圆点恢复成普通空心
func TestKilledTaskTurnsHollowAfterContinuation(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "killed", Description: "stopped"},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	red := lipgloss.NewStyle().Foreground(lipgloss.Color(redColor)).Render("◯")
	if !strings.Contains(m.taskRows()[1], red) {
		t.Fatalf("killed task should start red: %q", m.taskRows()[1])
	}

	// 续跑把状态改回 running
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "stopped"},
	}})
	row := m.taskRows()[1]
	if strings.Contains(row, red) {
		t.Fatalf("continuation kept the red dot: %q", row)
	}
	if !strings.Contains(ansi.Strip(row), "◯") {
		t.Fatalf("continuation row = %q, want hollow dot", ansi.Strip(row))
	}
}

// 列表聚焦时 Option+Up 仍要撤回 steering,不能被任务选择吃掉
func TestSteeringDequeueWorksWhileTaskFocused(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.busy = true
	m.state.Steering = []string{"queued"}
	m.handlers.RestoreSteering = func() []string { return []string{"queued"} }
	m.setTaskFocus(true)

	m.Update(tea.KeyMsg{Type: tea.KeyShiftLeft})
	if got := m.editor.Value(); got != "queued" {
		t.Fatalf("editor = %q, want the restored steering message", got)
	}
	if len(m.state.Steering) != 0 {
		t.Fatalf("steering not dequeued: %+v", m.state.Steering)
	}
}

// 列表聚焦时 Option+Down 仍要撤回 follow-up
func TestFollowUpDequeueWorksWhileTaskFocused(t *testing.T) {
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
	}}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.busy = true
	m.state.FollowUp = []string{"queued follow-up"}
	m.handlers.RestoreFollowUpMessages = func() []llm.UserMessage {
		return []llm.UserMessage{textUserMessage("queued follow-up")}
	}
	m.setTaskFocus(true)

	m.Update(tea.KeyMsg{Type: tea.KeyShiftRight})
	if got := m.editor.Value(); got != "queued follow-up" {
		t.Fatalf("editor = %q, want the restored follow-up", got)
	}
	if len(m.state.FollowUp) != 0 {
		t.Fatalf("follow-up not dequeued: %+v", m.state.FollowUp)
	}
}

// 子任务视图里队列撤回作用于该任务自己的队列
func TestSubagentViewDequeueTargetsOwnQueue(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "running", Description: "one"},
	}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("a1")
	m.setTaskFocus(true)

	m.Update(tea.KeyMsg{Type: tea.KeyShiftLeft})
	if len(requests) != 1 || requests[0].Kind != "dequeue" || requests[0].TaskID != "a1" || requests[0].FollowUp {
		t.Fatalf("subagent steering dequeue = %#v", requests)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyShiftRight})
	if len(requests) != 2 || requests[1].Kind != "dequeue" || !requests[1].FollowUp {
		t.Fatalf("subagent follow-up dequeue = %#v", requests)
	}
}

// 已停止任务的 child view 仍需选中 main 后 Enter 退出
func TestSubagentViewMainEnterExitsStoppedTask(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "work", Status: "killed", Description: "stopped"},
	}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("a1")
	press(m, tea.KeyEsc)
	if m.viewingTaskID != "a1" || len(requests) != 0 {
		t.Fatalf("Esc changed stopped child view: view=%q requests=%#v", m.viewingTaskID, requests)
	}
	press(m, tea.KeyUp)
	press(m, tea.KeyEnter)
	if m.viewingTaskID != "" || len(requests) != 1 || requests[0].Kind != "exit" {
		t.Fatalf("main Enter did not exit stopped child: view=%q requests=%#v", m.viewingTaskID, requests)
	}
}

// 列表失焦后光标回到 main 行,不能出现全都没有选中的状态
func TestTaskListNeverHasEmptySelection(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "explore", Status: "running", Description: "two"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.enterSubagentView("a1")
	m.setTaskFocus(false)

	rows := ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if strings.Count(rows, "❯") != 0 {
		t.Fatalf("blurred list should not render a cursor: %q", rows)
	}
	// 正在查看的任务保持实心,其余空心
	if !strings.Contains(rows, "◯ main") || !strings.Contains(rows, "⏺ explore  one") {
		t.Fatalf("blurred list markers = %q", rows)
	}

	// 重新聚焦时一定有选中行
	m.setTaskFocus(true)
	rows = ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if strings.Count(rows, "❯") != 1 {
		t.Fatalf("focused list must select exactly one row: %q", rows)
	}
}

// 进入 child view 后输入框保持焦点,当前 child 只有实心圆没有箭头
func TestEnterSubagentViewSelectsTask(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "explore", Status: "running", Description: "two"},
	}
	var requests []SubagentRequest
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.enterSubagentView("a1")

	if m.viewingTaskID != "a1" || m.selectedTaskID != "a1" || m.mainSelected || m.taskSelector || !m.editor.Focused() {
		t.Fatalf("viewing=%q selected=%q main=%v selector=%v focused=%v", m.viewingTaskID, m.selectedTaskID, m.mainSelected, m.taskSelector, m.editor.Focused())
	}
	rows := ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if !strings.Contains(rows, "  ⏺ explore  one") || strings.Contains(rows, "❯") {
		t.Fatalf("entered view rows = %q", rows)
	}

	// Up 进入列表并回到 main 行,这是 child view 返回主 agent 的选择路径
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if !m.taskSelector || !m.mainSelected || m.selectedTaskID != "" {
		t.Fatalf("up did not select main: selector=%v main=%v id=%q", m.taskSelector, m.mainSelected, m.selectedTaskID)
	}
	if !strings.Contains(ansi.Strip(strings.Join(m.taskRows(), "\n")), "❯ ⏺ main") {
		t.Fatalf("main selection row missing: %q", ansi.Strip(strings.Join(m.taskRows(), "\n")))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.viewingTaskID != "" || m.taskSelector || len(requests) != 1 || requests[0].Kind != "exit" || requests[0].TaskID != "a1" {
		t.Fatalf("main Enter did not return to main: view=%q selector=%v requests=%#v", m.viewingTaskID, m.taskSelector, requests)
	}
}

// 帧不得短于终端高度,否则底部会多出空白行
func TestFrameFillsTerminalHeight(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one", Messages: []Message{{ID: "m1", Role: "assistant", Text: "hi"}}},
		{ID: "a2", AgentType: "explore", Status: "running", Description: "two"},
	}
	for _, height := range []int{10, 13, 16, 24, 30} {
		m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
		m.Update(tea.WindowSizeMsg{Width: 100, Height: height})
		m.enterSubagentView("a1")
		lines := splitFrameLines(m.View())
		if len(lines) != height {
			t.Fatalf("height=%d rendered %d lines", height, len(lines))
		}
		// 最后一行必须是任务行,不能是补白
		if strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
			t.Fatalf("height=%d tail is padding: %q", height, ansi.Strip(lines[len(lines)-1]))
		}
	}
}

// 任务列表消失后输入框必须重新可输入,否则用户会卡在无法输入的状态
func TestInputRestoredWhenTaskListDisappears(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	// 1.聚焦任务列表
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if !m.taskSelector || m.editor.Focused() {
		t.Fatalf("list focus = %v editor focus = %v", m.taskSelector, m.editor.Focused())
	}
	// 2.任务全部被清理
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: nil})
	if m.taskSelector {
		t.Fatal("task list focus survived an empty snapshot")
	}
	if !m.editor.Focused() {
		t.Fatal("editor lost focus when the task list disappeared")
	}
	// 3.输入必须恢复
	typeText(m, "hi")
	if got := m.editor.Value(); got != "hi" {
		t.Fatalf("editor = %q, want hi", got)
	}
}

// 退出子任务视图后再 kill,输入框仍然可用
func TestInputWorksAfterExitThenKill(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one"},
	}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.enterSubagentView("a1")
	// Esc 停止并留在 child;选 main 后 Enter 回到主视图
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "a1", AgentType: "explore", Status: "killed", Description: "one"}}})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.viewingTaskID != "" {
		t.Fatalf("still viewing %q", m.viewingTaskID)
	}
	// 后端把任务标记为 killed
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "killed", Description: "one"},
	}})
	if !m.editor.Focused() || m.taskSelector {
		t.Fatalf("editor focus = %v selector = %v", m.editor.Focused(), m.taskSelector)
	}
	typeText(m, "still typing")
	if got := m.editor.Value(); got != "still typing" {
		t.Fatalf("editor = %q", got)
	}
}

// 队列区行数必须与渲染行数一致,否则撤回后会多出空行
func TestPendingRowsMatchRenderedLines(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one"},
		{ID: "a2", AgentType: "explore", Status: "running", Description: "two"},
	}
	for _, height := range []int{8, 12, 16, 24, 40} {
		for _, focused := range []bool{false, true} {
			m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
			m.Update(tea.WindowSizeMsg{Width: 100, Height: height})
			m.enterSubagentView("a1")
			m.setTaskFocus(focused)
			m.state.Steering = []string{"queued"}
			m.state.SteeringInputs = []llm.UserMessage{textUserMessage("queued")}
			m.layout()
			if got, want := m.pendingRows(), len(m.pendingLines()); got != want {
				t.Fatalf("h=%d focused=%v pendingRows=%d lines=%d", height, focused, got, want)
			}
		}
	}
}

// 子任务视图里撤回 steering 后帧不得变短或多出空行
func TestSubagentDequeueKeepsFrameFull(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one"},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	m.enterSubagentView("a1")
	m.state.Steering = []string{"queued steering"}
	m.state.SteeringInputs = []llm.UserMessage{textUserMessage("queued steering")}
	m.layout()
	before := len(splitFrameLines(m.View()))

	m.restoreSubagentMessages([]llm.UserMessage{textUserMessage("queued steering")})
	m.state.Steering = nil
	m.state.SteeringInputs = nil
	m.layout()
	lines := splitFrameLines(m.View())
	if len(lines) != before {
		t.Fatalf("frame height changed: %d -> %d", before, len(lines))
	}
	if strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		t.Fatalf("tail padding after dequeue:\n%s", ansi.Strip(strings.Join(lines, "\n")))
	}
}

// child view 输入框保持焦点,显式进入列表后 Tab 仍提交 follow-up
func TestSubagentViewFollowUpWithListFocused(t *testing.T) {
	var requests []SubagentRequest
	m := New(Snapshot{Subagents: SubagentSnapshot{Tasks: []SubagentTaskView{{
		ID: "a1", AgentType: "work", Status: "running", Description: "inspect",
	}}}}, Handlers{SubagentRequest: func(request SubagentRequest) tea.Cmd {
		requests = append(requests, request)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.enterSubagentView("a1")
	m.setTaskFocus(true)
	if !m.taskSelector {
		t.Fatal("explicit task focus did not open the task list")
	}

	m.editor.SetValue("later please")
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if len(requests) != 1 {
		t.Fatalf("requests = %#v, want one follow-up", requests)
	}
	if requests[0].Kind != "followup" || requests[0].TaskID != "a1" {
		t.Fatalf("request = %#v", requests[0])
	}
	if got := m.editor.Value(); got != "" {
		t.Fatalf("editor not cleared: %q", got)
	}
}

// 队列清空后必须重算布局,否则 viewport 变高但帧变短
func TestDequeueRelayoutsViewport(t *testing.T) {
	tasks := []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one", PendingFollowUp: []string{"queued"}},
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	m.enterSubagentView("a1")
	m.layout()
	if len(splitFrameLines(m.View())) != 16 {
		t.Fatalf("frame before dequeue = %d", len(splitFrameLines(m.View())))
	}

	m.restoreSubagentMessages([]llm.UserMessage{textUserMessage("queued")})
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{
		{ID: "a1", AgentType: "explore", Status: "running", Description: "one"},
	}})
	lines := splitFrameLines(m.View())
	if len(lines) != 16 {
		t.Fatalf("frame after dequeue = %d, want 16", len(lines))
	}
	if strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		t.Fatalf("tail padding after dequeue:\n%s", ansi.Strip(strings.Join(lines, "\n")))
	}
	if m.taskSelector {
		t.Fatal("dequeue should keep child input focus")
	}
}

// 任务多到会挤掉输入坞时必须截断列表,否则用户看不到自己输入的内容
func TestManyTasksKeepEditorVisible(t *testing.T) {
	for _, count := range []int{5, 10, 20, 40} {
		for _, focused := range []bool{false, true} {
			var tasks []SubagentTaskView
			for i := 0; i < count; i++ {
				tasks = append(tasks, SubagentTaskView{
					ID:          fmt.Sprintf("t-%d", i),
					AgentType:   "explore",
					Status:      "completed",
					Description: "task",
				})
			}
			m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
			m.setTaskFocus(focused)
			typeText(m, "draft")

			// 行数预算必须与实际渲染一致
			if got, want := m.subagentRows(), len(m.taskRows()); got != want {
				t.Fatalf("count=%d focused=%v subagentRows=%d rendered=%d", count, focused, got, want)
			}
			// 输入框必须留在帧内
			lines := splitFrameLines(m.View())
			visible := false
			for _, line := range lines {
				if strings.Contains(ansi.Strip(line), "draft") {
					visible = true
					break
				}
			}
			if !visible {
				t.Fatalf("count=%d focused=%v editor pushed out of frame", count, focused)
			}
		}
	}
}

// 截断时保留最近的任务,main 行始终在
func TestTaskListTruncationKeepsNewest(t *testing.T) {
	var tasks []SubagentTaskView
	for i := 0; i < 30; i++ {
		tasks = append(tasks, SubagentTaskView{
			ID:          fmt.Sprintf("t-%d", i),
			AgentType:   "explore",
			Status:      "completed",
			Description: fmt.Sprintf("task-%d", i),
			StartTime:   time.Unix(int64(i), 0),
		})
	}
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{Tasks: tasks}}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	rows := ansi.Strip(strings.Join(m.taskRows(), "\n"))
	if !strings.Contains(rows, "main") {
		t.Fatalf("main row dropped:\n%s", rows)
	}
	if !strings.Contains(rows, "task-29") {
		t.Fatalf("newest task dropped:\n%s", rows)
	}
	if strings.Contains(rows, "task-0 ") || strings.Contains(rows, "task-1 ") {
		t.Fatalf("oldest tasks should be truncated:\n%s", rows)
	}
}

func TestSubagentErrorResponseAppliesFreshSnapshot(t *testing.T) {
	m := New(Snapshot{Subagents: SubagentSnapshot{
		Revision: 1,
		Tasks:    []SubagentTaskView{{ID: "a1", Type: "agent", Status: "running", Description: "finished task"}},
	}}, Handlers{})
	m.Update(teaWindow(100, 16))
	m.Update(SubagentSnapshotMsg{
		Err: errors.New("task finished before stop"),
		Snapshot: SubagentSnapshot{
			Revision: 2,
			Tasks:    []SubagentTaskView{{ID: "a1", Type: "agent", Status: "completed", Description: "finished task"}},
		},
	})
	task, ok := findSubagentTask(m.state.Subagents.Tasks, "a1")
	if !ok || task.Status != "completed" {
		t.Fatalf("task snapshot = %+v, want completed task", task)
	}
}
