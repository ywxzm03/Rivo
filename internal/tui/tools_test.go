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

func TestCompactToolGroupShowsLatestThreeAndThinking(t *testing.T) {
	group := testToolGroup(5)
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "checking the failing assertion", Tokens: 1200}}
	got := ansi.Strip(renderToolGroup(group, 120, false, time.UnixMilli(1000)))
	for _, want := range []string{"cmd", "… +2 more", "thinking:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact group missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "bash") != 3 {
		t.Fatalf("compact group rendered %d tools, want 3:\n%s", strings.Count(got, "bash"), got)
	}
}

func TestCompactToolGroupMarksCompletedActiveGroupDone(t *testing.T) {
	group := testToolGroup(1)
	got := ansi.Strip(renderToolGroup(group, 80, false, time.UnixMilli(1000)))
	if !strings.Contains(got, "✓ tools done") {
		t.Fatalf("completed tool group should be done = %q", got)
	}
	group.Active, group.Sealed = false, true
	got = ansi.Strip(renderToolGroup(group, 80, false, time.UnixMilli(1000)))
	if !strings.Contains(got, "✓ tools done") {
		t.Fatalf("sealed group should be done = %q", got)
	}
}

func TestUnsealedThinkingGroupStaysWorkingAfterThinkingEnd(t *testing.T) {
	group := &ToolGroupView{
		ID:     "group",
		Sealed: false,
		Thinking: []ThinkingBlockView{{
			Text:   "still reasoning",
			Active: true,
		}},
		Calls: []ToolCallView{{Name: "bash", Status: ToolSuccess}},
	}
	got := ansi.Strip(renderToolGroup(group, 80, false, time.UnixMilli(1000)))
	if !strings.Contains(got, "thinking...") || strings.Contains(got, "tools done") {
		t.Fatalf("unsealed thinking group = %q", got)
	}
	group.Thinking[0].Active = false
	got = ansi.Strip(renderToolGroup(group, 80, false, time.UnixMilli(1000)))
	if !strings.Contains(got, "✓ tools done") {
		t.Fatalf("completed thinking group should be done = %q", got)
	}
}

func TestEmptySealedToolGroupIsNotRendered(t *testing.T) {
	group := &ToolGroupView{ID: "empty", Sealed: true}
	if got := renderToolGroup(group, 80, false, time.UnixMilli(1000)); got != "" {
		t.Fatalf("empty sealed group = %q", got)
	}
}

func TestExpandedToolGroupLimitsResultAndKeepsThinking(t *testing.T) {
	group := testToolGroup(1)
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "full thinking\nsecond line", Tokens: 1000}}
	group.Calls[0].Result = strings.Repeat("line\n", 20)
	got := ansi.Strip(renderToolGroup(group, 80, true, time.UnixMilli(1000)))
	if !strings.Contains(got, "…") || !strings.Contains(got, "full thinking") || !strings.Contains(got, "second line") {
		t.Fatalf("expanded group did not apply result/thinking policy:\n%s", got)
	}
}

func TestModelToolGroupUsesMarkdownPreviewAndCtrlOToggle(t *testing.T) {
	group := testToolGroup(1)
	group.Calls[0].Result = "```go\nfmt.Println(\"ok\")\n```"
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "reasoning **detail**", Tokens: 1000}}
	m := New(Snapshot{Messages: []Message{{ID: "tools", Role: "assistant", ToolGroup: group}}}, Handlers{})
	m.width = 80
	compact := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(compact, "thinking:") || strings.Contains(compact, "fmt.Println") {
		t.Fatalf("compact group = %q", compact)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	expanded := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(expanded, "fmt.Println") || !strings.Contains(expanded, "reasoning detail") {
		t.Fatalf("expanded group = %q", expanded)
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
	if len(m.spinner.Spinner.Frames) != len(compactSpinner) {
		t.Fatalf("spinner frame count = %d, want %d", len(m.spinner.Spinner.Frames), len(compactSpinner))
	}
	for index, frame := range compactSpinner {
		if m.spinner.Spinner.Frames[index] != frame {
			t.Fatalf("spinner frame %d = %q, want %q", index, m.spinner.Spinner.Frames[index], frame)
		}
	}
}
