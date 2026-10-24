package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestShortenToolPathRequiresHomeBoundary(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	separator := string(os.PathSeparator)
	if got := shortenToolPath(home); got != "~" {
		t.Fatalf("home path = %q, want ~", got)
	}
	if got := shortenToolPath(home + separator + "project"); got != "~"+separator+"project" {
		t.Fatalf("child path = %q, want home-relative path", got)
	}
	if got := shortenToolPath(home + "-backup/project"); got != home+"-backup/project" {
		t.Fatalf("lookalike path = %q, want unchanged", got)
	}
}

func testToolGroup(count int) *ToolGroupView {
	group := &ToolGroupView{ID: "group", Active: true}
	for i := 0; i < count; i++ {
		group.Calls = append(group.Calls, ToolCallView{ID: string(rune('a' + i)), Name: "bash", Arguments: `{"command":"cmd"}`, Result: "result", Status: ToolSuccess, Elapsed: 100 * time.Millisecond})
	}
	return group
}

// 每条调用都单独成块，不再只留最近三条，也没有组头状态行
func TestToolRowGroupShowsEveryCall(t *testing.T) {
	group := testToolGroup(5)
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "checking the failing assertion", Tokens: 1200}}
	got := ansi.Strip(renderToolGroup(group, 120, false, time.UnixMilli(1000)))
	if strings.Count(got, "⦁ Bash(cmd)") != 5 {
		t.Fatalf("group rendered %d calls, want 5:\n%s", strings.Count(got, "⦁ Bash(cmd)"), got)
	}
	for _, unwanted := range []string{"tools done", "… +", "thinking:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("group still shows %q:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "Thinking...") {
		t.Fatalf("thinking line missing:\n%s", got)
	}
}

// 思考是否结束都不改变工具行的外观：只有一行 Thinking...
func TestThinkingLineIgnoresActiveState(t *testing.T) {
	group := &ToolGroupView{
		ID:       "group",
		Thinking: []ThinkingBlockView{{Text: "still reasoning", Active: true}},
		Calls:    []ToolCallView{{Name: "bash", Status: ToolSuccess}},
	}
	active := ansi.Strip(renderToolGroup(group, 80, false, time.UnixMilli(1000)))
	group.Thinking[0].Active = false
	done := ansi.Strip(renderToolGroup(group, 80, false, time.UnixMilli(1000)))
	if active != done || !strings.Contains(active, "Thinking...") {
		t.Fatalf("thinking line changed with state: %q vs %q", active, done)
	}
}

func TestEmptySealedToolGroupIsNotRendered(t *testing.T) {
	group := &ToolGroupView{ID: "empty", Sealed: true}
	if got := renderToolGroup(group, 80, false, time.UnixMilli(1000)); got != "" {
		t.Fatalf("empty sealed group = %q", got)
	}
}

// 展开后命令输出给全文；思考由 Ctrl+T 单独展开，不随工具行一起打开
func TestExpandedToolGroupShowsFullOutput(t *testing.T) {
	group := testToolGroup(1)
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "full thinking\nsecond line", Tokens: 1000}}
	group.Calls[0].Result = strings.Repeat("line\n", 20)
	got := ansi.Strip(renderToolGroup(group, 80, true, time.UnixMilli(1000)))
	if strings.Contains(got, "ctrl+o") {
		t.Fatalf("expanded output was folded:\n%s", got)
	}
	if strings.Count(got, "line") < 20 {
		t.Fatalf("expanded group lost output:\n%s", got)
	}
	if !strings.Contains(got, "Thinking...") || strings.Contains(got, "full thinking") {
		t.Fatalf("Ctrl+O also opened the thinking block:\n%s", got)
	}
	expandedThinking := ansi.Strip(renderToolGroupWithState(group, 80, toolRenderState{allExpanded: true, showThinking: true}, time.UnixMilli(1000)))
	if !strings.Contains(expandedThinking, "full thinking") || !strings.Contains(expandedThinking, "second line") {
		t.Fatalf("Ctrl+T did not open the thinking block:\n%s", expandedThinking)
	}
}

// Ctrl+O 展开命令输出与思考正文，收起时只有结果句和 Thinking...
func TestModelCtrlOTogglesToolOutputAndThinking(t *testing.T) {
	group := testToolGroup(1)
	group.Calls[0].Result = "first\nsecond\nthird\nfourth\nfifth"
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "reasoning **detail**", Tokens: 1000}}
	m := New(Snapshot{Messages: []Message{{ID: "tools", Role: "assistant", ToolGroup: group}}}, Handlers{})
	m.width = 80
	compact := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(compact, "Thinking...") || strings.Contains(compact, "fifth") || strings.Contains(compact, "reasoning") {
		t.Fatalf("compact group = %q", compact)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	expanded := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(expanded, "fifth") {
		t.Fatalf("Ctrl+O did not open the output: %q", expanded)
	}
	if strings.Contains(expanded, "reasoning") {
		t.Fatalf("Ctrl+O also opened thinking: %q", expanded)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	thought := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(thought, "reasoning **detail**") {
		t.Fatalf("Ctrl+T did not open thinking: %q", thought)
	}
}

func TestActiveToolGroupRefreshesWithWorkingSpinner(t *testing.T) {
	group := testToolGroup(1)
	group.Active = true
	group.Calls[0].Status = ToolPending
	m := New(Snapshot{Messages: []Message{{ID: "tools", Role: "assistant", ToolGroup: group}}}, Handlers{})
	m.width = 80
	m.busy = true
	now := time.UnixMilli(1000)
	m.clock = func() time.Time { return now }
	m.refreshTranscript()
	first := m.cache["tools"].view
	now = now.Add(100 * time.Millisecond)
	m.Update(spinner.TickMsg{})
	second := m.cache["tools"].view
	if first == second {
		t.Fatal("active tool group did not refresh on spinner tick")
	}
}

func TestModelUsesPiCompactSpinnerFrames(t *testing.T) {
	m := New(Snapshot{}, Handlers{})
	if len(m.spinner.Spinner.Frames) != len(statusSpinner) {
		t.Fatalf("spinner frame count = %d, want %d", len(m.spinner.Spinner.Frames), len(statusSpinner))
	}
	for index, frame := range statusSpinner {
		if m.spinner.Spinner.Frames[index] != frame {
			t.Fatalf("spinner frame %d = %q, want %q", index, m.spinner.Spinner.Frames[index], frame)
		}
	}
}
