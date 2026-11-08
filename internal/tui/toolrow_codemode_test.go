package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func codemodeRow() ToolCallView {
	call := rowCall("parent", "codemode", map[string]any{"code": "const sourceOnly = 1;\ntext(sourceOnly);"}, ToolSuccess)
	call.Result = "one\ntwo\nthree\nfour\nfive"
	call.Codemode = &CodemodeView{
		Calls: []CodemodeCallView{
			{ID: "parent/1", Name: "edit", Args: `{"path":"first.go"}`, Status: CodemodeOK, DurationMS: new(int64(10))},
			{ID: "parent/2", Name: "read", Args: `{"path":"second.go"}`, Status: CodemodeOK, DurationMS: new(int64(20))},
		},
		Summary: CodemodeSummaryView{Complete: true, Groups: []CodemodeToolCountView{{Name: "read", Success: 1}, {Name: "edit", Success: 1}}},
		Output:  CodemodeOutputView{DurationMS: new(int64(1234)), LineCount: 120, FullOutputPath: "/tmp/codemode-output.txt"},
	}
	return call
}

func TestCodemodeFooterDurationBoundariesAndZeroLines(t *testing.T) {
	for _, tc := range []struct {
		ms   int64
		want string
	}{
		{0, "0.000s"}, {59999, "59.999s"}, {60000, "1m 0s"}, {3599999, "59m 59s"}, {3600000, "1h 0m 0s"}, {3661999, "1h 1m 1s"},
	} {
		t.Run(fmt.Sprint(tc.ms), func(t *testing.T) {
			call := codemodeRow()
			call.Result = ""
			call.Codemode.Output = CodemodeOutputView{DurationMS: new(tc.ms), LineCount: 0}
			call.Codemode.Calls = nil
			call.Codemode.Summary.Groups = nil
			got := strings.Join(rowLines(call, false), "\n")
			if !strings.Contains(got, "Done in "+tc.want+" (0 lines)") || !strings.Contains(got, "(No content)") {
				t.Fatalf("zero output footer:\n%s", got)
			}
		})
	}
}

func TestCodemodeSuccessfulExpansionKeepsIssuanceOrderAndSingleTimer(t *testing.T) {
	call := codemodeRow()
	got := strings.Join(rowLines(call, true), "\n")
	for _, want := range []string{"120 lines", "Calls", `edit {"path":"first.go"}`, `read {"path":"second.go"}`, "Output", "one", "five", "/tmp/codemode-output.txt", "Done in 1.234s (120 lines)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expanded output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "first.go") > strings.Index(got, "second.go") {
		t.Fatalf("Calls reordered by group:\n%s", got)
	}
	if strings.Count(got, "1.234s") != 1 || strings.Count(got, "sourceOnly") != 1 || strings.Contains(got, "text(sourceOnly)") || strings.Contains(got, "ctrl+o") {
		t.Fatalf("expanded output duplicates timer or source:\n%s", got)
	}
	compact := strings.Join(rowLines(call, false), "\n")
	preview, path, footer, hint := strings.Index(compact, "three"), strings.Index(compact, "/tmp/codemode-output.txt"), strings.Index(compact, "Done"), strings.Index(compact, "+2 lines")
	if preview < 0 || preview >= path || path >= footer || footer >= hint {
		t.Fatalf("preview/path/footer/hint order:\n%s", compact)
	}
}

func TestCodemodeFailedExpansionPreservesPartialOutputAndErrorOnce(t *testing.T) {
	call := codemodeRow()
	call.Status = ToolError
	call.Result = "before-one\nbefore-two"
	call.Codemode.Error = "Error: boom\n    at codemode.js:3:7\n    at main"
	got := strings.Join(rowLines(call, true), "\n")
	for _, want := range []string{"Error: boom", "before-one", "before-two", "at codemode.js:3:7", "at main", "Failed in 1.234s"} {
		if strings.Count(got, want) != 1 {
			t.Fatalf("failed output must contain %q once:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"Calls", "Output\n", "120 lines", "first.go", "(120 lines)", "Done"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("failure contains success detail %q:\n%s", forbidden, got)
		}
	}
	if strings.Index(got, "Error: boom") > strings.Index(got, "before-one") || strings.Index(got, "before-two") > strings.Index(got, "at codemode.js") {
		t.Fatalf("failed title/output/stack order:\n%s", got)
	}
}

func TestCodemodeFooterCaughtChildErrorAndPartialGroups(t *testing.T) {
	call := codemodeRow()
	call.Codemode.Summary = CodemodeSummaryView{Complete: true, Groups: []CodemodeToolCountView{
		{Name: "read", Success: 2, Failure: 1, Unfinished: 2}, {Name: "bash", Unfinished: 1}, {Name: "edit", Success: 3},
	}}
	got := strings.Join(rowLines(call, false), "\n")
	for _, want := range []string{"Done in 1.234s", "read 2 1 2 unfinished", "bash 1", "edit 3", "(partial)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("group footer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Failed") {
		t.Fatalf("caught child error changed parent status:\n%s", got)
	}
	call.Codemode.Summary.Groups = nil
	call.Codemode.Summary.Complete = false
	if got := strings.Join(rowLines(call, false), "\n"); !strings.Contains(got, "(partial)") {
		t.Fatalf("incomplete empty snapshot:\n%s", got)
	}
}

func TestCodemodeWidthsUnicodeEmojiAndANSI(t *testing.T) {
	for _, width := range []int{1, 3, 8, 20, 44, 120} {
		for _, expanded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/expanded=%v", width, expanded), func(t *testing.T) {
				call := codemodeRow()
				call.Result = "\x1b[31m你好👩‍💻é\x1b[0m alpha\nsecond\nthird\nfourth\nfifth"
				lines := renderToolRow(call, width, expanded, time.Unix(0, 0))
				for _, line := range lines {
					if got := ansi.StringWidth(line); got > width {
						t.Fatalf("row width %d > %d: %q", got, width, line)
					}
					if strings.ContainsRune(ansi.Strip(line), '\uFFFD') {
						t.Fatalf("broken Unicode: %q", line)
					}
				}
				joined := strings.Join(plainLines(strings.Join(lines, "\n")), "")
				flat := strings.Join(strings.Fields(joined), "")
				if !strings.Contains(flat, "Donein1.234s(120lines)") {
					t.Fatalf("footer was clipped: %q", joined)
				}
				if expanded && !strings.Contains(flat, "fifth") {
					t.Fatalf("expanded body lost final output: %q", joined)
				}
			})
		}
	}
}

func TestCodemodePendingRunningAndUnknownMetadata(t *testing.T) {
	call := codemodeRow()
	call.Status = ToolPending
	if got := strings.Join(rowLines(call, false), "\n"); strings.Contains(got, "Done") || strings.Contains(got, "one") || strings.Contains(got, "Running") {
		t.Fatalf("unstarted call has body:\n%s", got)
	}
	call.StartedAt = time.Unix(1, 0)
	for _, expanded := range []bool{false, true} {
		got := strings.Join(rowLines(call, expanded), "\n")
		if !strings.Contains(got, "Running") || strings.Contains(got, "Done") || strings.Contains(got, "Calls") || strings.Contains(got, "1.234s") {
			t.Fatalf("running call exposes final data:\n%s", got)
		}
	}
	call.Status = ToolSuccess
	call.Codemode = nil
	call.Result = "legacy body"
	got := strings.Join(rowLines(call, false), "\n")
	if !strings.Contains(got, "legacy body") || strings.Contains(got, "0 lines") || strings.Contains(got, "0.000s") {
		t.Fatalf("unknown metadata fabricated:\n%s", got)
	}
}

func TestCodemodeCloneIsolatesCallsGroupsAndDurations(t *testing.T) {
	original := &ToolGroupView{ID: "g", Calls: []ToolCallView{codemodeRow()}}
	want := &ToolGroupView{ID: "g", Calls: []ToolCallView{codemodeRow()}}
	cloned := original.Clone()
	cloned.Calls[0].Codemode.Calls[0].Name = "changed"
	*cloned.Calls[0].Codemode.Calls[0].DurationMS = 99
	cloned.Calls[0].Codemode.Summary.Groups[0].Success = 88
	*cloned.Calls[0].Codemode.Output.DurationMS = 77
	cloned.Calls[0].Codemode.Error = "changed"
	if !reflect.DeepEqual(original, want) {
		t.Fatalf("clone mutation changed published original: %+v", original.Calls[0].Codemode)
	}
	if cloned.Calls[0].Codemode == original.Calls[0].Codemode {
		t.Fatal("Codemode view still shared")
	}
}

func TestCodemodeModelEventsParentHitAndCtrlO(t *testing.T) {
	call := codemodeRow()
	m := New(Snapshot{}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(MessageMsg(Message{ID: "message", Role: "assistant", Revision: 1, ToolGroup: &ToolGroupView{ID: "g", Calls: []ToolCallView{call}}}))
	m.refreshTranscript()
	if len(m.transcript.hits) != 1 || m.transcript.hits[0].Kind != "call" || m.transcript.hits[0].Key != "parent" {
		t.Fatalf("nested calls created transcript hits: %+v", m.transcript.hits)
	}
	if strings.Contains(ansi.Strip(m.renderMessage(m.state.Messages[0])), "five") {
		t.Fatal("initial result already expanded")
	}
	if !m.toggleToolHit(m.transcript.hits[0].Start) {
		t.Fatal("parent hit not interactive")
	}
	if got := ansi.Strip(m.renderMessage(m.state.Messages[0])); !strings.Contains(got, "five") || !strings.Contains(got, "first.go") {
		t.Fatalf("parent hit did not expand Calls and output:\n%s", got)
	}
	m.toggleToolHit(m.transcript.hits[0].Start)
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if got := ansi.Strip(m.renderMessage(m.state.Messages[0])); !strings.Contains(got, "five") {
		t.Fatalf("Ctrl+O did not invalidate cached collapse:\n%s", got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if got := ansi.Strip(m.renderMessage(m.state.Messages[0])); strings.Contains(got, "five") {
		t.Fatalf("Ctrl+O did not collapse:\n%s", got)
	}
}

func TestCodemodeBudgetEmptyOutputStillShowsSpillAndAuthoritativeLines(t *testing.T) {
	call := codemodeRow()
	call.Result = ""
	for _, expanded := range []bool{false, true} {
		got := strings.Join(rowLines(call, expanded), "\n")
		if !strings.Contains(got, "/tmp/codemode-output.txt") || !strings.Contains(got, "(120 lines)") || strings.Contains(got, "(No content)") {
			t.Fatalf("budget-empty output misreported as zero output:\n%s", got)
		}
	}
	call.Status = ToolInterrupted
	call.Codemode.Output.DurationMS = nil
	call.Codemode.Summary.Complete = false
	got := strings.Join(rowLines(call, false), "\n")
	if strings.Contains(got, "Done") || strings.Contains(got, "1.234s") || !strings.Contains(got, "(partial)") {
		t.Fatalf("interrupted call fabricated completion:\n%s", got)
	}
}

func TestCodemodeExpandedCallStatusesAndUnshortenedArguments(t *testing.T) {
	call := codemodeRow()
	longArgs := `{"path":"` + strings.Repeat("界", 90) + `"}`
	call.Codemode.Calls = []CodemodeCallView{
		{ID: "parent/1", Name: "queuedTool", Args: `{}`, Status: CodemodeQueued},
		{ID: "parent/2", Name: "runningTool", Args: `{}`, Status: CodemodeRunning},
		{ID: "parent/3", Name: "okTool", Args: longArgs, Status: CodemodeOK, DurationMS: new(int64(0))},
		{ID: "parent/4", Name: "failedTool", Args: `{}`, Status: CodemodeError, Error: "caught failure"},
		{ID: "parent/5", Name: "cancelledTool", Args: `{}`, Status: CodemodeCancelled},
	}
	lines := plainLines(strings.Join(renderToolRow(call, 400, true, time.Unix(0, 0)), "\n"))
	got := strings.Join(lines, "\n")
	for _, want := range []string{"… queuedTool", "… runningTool", "✓ okTool " + longArgs, "0.000s", "✗ failedTool", "caught failure", "⊘ cancelledTool"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expanded Calls missing %q:\n%s", want, got)
		}
	}
}
