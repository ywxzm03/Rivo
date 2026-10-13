package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
)

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

func testState() Snapshot {
	s := Snapshot{Cwd: "/project/Rivo", Branch: "feat(tui)/fullscreen-tui", Thinking: "high", ContextKnown: true, AutoCompact: true}
	for i := 0; i < 12; i++ {
		s.Models = append(s.Models, ModelOption{ID: fmt.Sprintf("model-%02d", i), Provider: fmt.Sprintf("provider-%d", i%2), Name: fmt.Sprintf("Model %d", i), ContextWindow: 1000000, Levels: []string{"off", "low", "high"}})
	}
	s.ModelKey, s.DefaultModelKey = s.Models[0].Key(), s.Models[1].Key()
	s.Sessions = []SessionOption{{Path: "one", Summary: "First session", Modified: "2m ago"}, {Path: "two", Summary: "中文会话", Modified: "1h ago"}}
	return s
}

func testModel(width, height int) *Model {
	m := New(testState(), Handlers{})
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func typeText(m *Model, value string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)})
}

// pasteText 模拟终端 bracketed paste，整段内容以单条 KeyMsg 进入
func pasteText(m *Model, value string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value), Paste: true})
}
func press(m *Model, key tea.KeyType) tea.Cmd {
	_, cmd := m.Update(tea.KeyMsg{Type: key})
	return cmd
}

// assertFrame 检查组件原始高度，避免最终截断掩盖布局溢出
func assertFrame(t *testing.T, m *Model) {
	t.Helper()
	dock := m.editor.Height() + 2 + len(m.completionItems())
	if m.selector != nil {
		dock = len(strings.Split(m.selector.view(m.width, m.height-3), "\n"))
		if dock != m.selector.height(m.height-3) {
			t.Fatalf("selector measured %d, rendered %d", m.selector.height(m.height-3), dock)
		}
	}
	if got := m.viewport.Height + dock + 2; got != m.height {
		t.Fatalf("component height = %d, terminal = %d", got, m.height)
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.height {
		t.Fatalf("frame height = %d, want %d", len(lines), m.height)
	}
	for i, line := range lines {
		if width := ansi.StringWidth(line); width > m.width {
			t.Fatalf("line %d width = %d, want <= %d", i, width, m.width)
		}
	}
	if !strings.Contains(ansi.Strip(lines[len(lines)-2]), "/project/Rivo") {
		t.Fatal("cwd footer moved or disappeared")
	}
}

func TestLayout(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {80, 24}, {60, 15}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := testModel(size[0], size[1])
			m.state.Messages = []Message{{ID: "u", Role: "user", Text: strings.Repeat("中文 👋 **bold** ", 30)}, {ID: "a", Role: "assistant", Thinking: "Let me think", Text: "```go\n" + strings.Repeat("long_code", 40), Elapsed: 12 * time.Second}}
			m.refreshTranscript()
			assertFrame(t, m)
			for i, line := range strings.Split(m.renderMessage(m.state.Messages[0]), "\n") {
				prefix := "  "
				if i == 0 {
					prefix = "❯ "
				}
				if !strings.HasPrefix(ansi.Strip(line), prefix) {
					t.Fatalf("user row %d must start with %q: %q", i, prefix, line)
				}
				if ansi.StringWidth(line) != m.width {
					t.Fatalf("user background width %d, want %d", ansi.StringWidth(line), m.width)
				}
			}
			typeText(m, strings.Repeat("中文 words 👋\n", 15))
			assertFrame(t, m)
			press(m, tea.KeyCtrlC)
			typeText(m, "/")
			assertFrame(t, m)
			if len(m.completionItems()) != 5 {
				t.Fatal("slash must show five supported commands")
			}
			for _, kind := range []string{"model", "thinking", "resume"} {
				m.openSelector(kind, "")
				assertFrame(t, m)
				typeText(m, "no-such-match-xyz")
				assertFrame(t, m)
				press(m, tea.KeyEsc)
			}
		})
	}
}

func TestEditorWrapHistoryAndBusyDraft(t *testing.T) {
	m := testModel(60, 15)
	typeText(m, strings.Repeat("a", 59))
	if m.editor.Height() != 2 {
		t.Fatal("exact-width input must reserve the cursor row")
	}
	inputView := strings.Split(strings.TrimSuffix(ansi.Strip(m.editor.View()), "\n"), "\n")
	if len(inputView) < 2 || !strings.HasPrefix(inputView[0], "❯ ") {
		t.Fatalf("wrapped input hid its first row: %q", inputView)
	}
	press(m, tea.KeyCtrlJ)
	typeText(m, "second")
	if !strings.Contains(m.editor.Value(), "\nsecond") || m.editor.Height() != 3 {
		t.Fatalf("newline layout: %q height %d", m.editor.Value(), m.editor.Height())
	}
	press(m, tea.KeyEnter)
	typeText(m, "draft")
	press(m, tea.KeyUp)
	if !strings.HasSuffix(m.editor.Value(), "second") {
		t.Fatal("history entry missing")
	}
	press(m, tea.KeyDown)
	if m.editor.Value() != "draft" {
		t.Fatal("history lost unsubmitted draft")
	}
	m.busy = true
	press(m, tea.KeyEnter)
	if m.editor.Value() != "draft" || !m.busy {
		t.Fatal("busy submission must preserve draft and operation")
	}
}

func TestEditorViewportReanchorsAfterGrowth(t *testing.T) {
	m := testModel(80, 30)
	typeText(m, strings.Repeat("a", 500))
	lines := strings.Split(strings.TrimSuffix(ansi.Strip(m.editor.View()), "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "❯ ") {
		t.Fatalf("editor viewport hid the first wrapped row: %q", lines[:min(3, len(lines))])
	}
	if m.editor.Line() != 0 || m.editor.LineInfo().CharOffset == 0 {
		t.Fatalf("cursor moved while reanchoring: line=%d info=%+v", m.editor.Line(), m.editor.LineInfo())
	}
}

func TestCompletionAndSelectors(t *testing.T) {
	m := testModel(120, 30)
	var requests []Request
	m.handlers.Request = func(r Request) tea.Cmd { requests = append(requests, r); return nil }
	typeText(m, "/mod")
	press(m, tea.KeyTab)
	if m.editor.Value() != "/model " || m.selector != nil {
		t.Fatal("Tab must only complete")
	}
	press(m, tea.KeyEnter)
	if m.selector == nil || m.editor.Focused() {
		t.Fatal("selector did not take focus")
	}
	typeText(m, "model-07")
	if len(m.selector.filtered) != 1 {
		t.Fatal("model search did not filter")
	}
	press(m, tea.KeyEnter)
	if m.selector != nil || !m.editor.Focused() || len(requests) != 1 || requests[0].Value != "provider-1/model-07" {
		t.Fatalf("selection: %+v", requests)
	}
	m.Update(OperationDoneMsg{})
	typeText(m, "/model model-03")
	press(m, tea.KeyEnter)
	if len(requests) != 2 || requests[1].Value != "provider-1/model-03" || m.selector != nil {
		t.Fatal("exact model match must switch directly")
	}
	m.Update(OperationDoneMsg{})
	m.openSelector("model", "absent-model")
	press(m, tea.KeyEnter)
	if m.selector == nil || len(requests) != 2 {
		t.Fatal("empty selection dispatched")
	}
	press(m, tea.KeyCtrlC)
	if m.selector != nil || !m.editor.Focused() || m.closing {
		t.Fatal("Ctrl+C should cancel selector")
	}
	typeText(m, "/thin")
	press(m, tea.KeyEnter)
	if m.selector == nil || m.selector.kind != "thinking" {
		t.Fatal("Enter must complete and execute")
	}
}

func TestCancelAndExit(t *testing.T) {
	m := testModel(80, 24)
	cancelled := 0
	m.handlers.Cancel = func() { cancelled++ }
	m.busy = true
	typeText(m, "/")
	press(m, tea.KeyEsc)
	if cancelled != 0 || len(m.completionItems()) != 0 {
		t.Fatal("first Escape must dismiss completion")
	}
	press(m, tea.KeyEsc)
	if cancelled != 1 || !m.busy {
		t.Fatal("cancel should signal and await operation completion")
	}
	typeText(m, "draft")
	press(m, tea.KeyCtrlD)
	if m.closing {
		t.Fatal("Ctrl+D with text must not exit")
	}
	now := time.Unix(1000, 0)
	m.clock = func() time.Time { return now }
	press(m, tea.KeyCtrlC)
	if m.editor.Value() != "" || cancelled != 1 {
		t.Fatal("first Ctrl+C should clear only")
	}
	now = now.Add(200 * time.Millisecond)
	cmd := press(m, tea.KeyCtrlC)
	if !m.closing || cancelled != 2 {
		t.Fatal("second Ctrl+C must exit even while busy")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("missing quit command")
	}
	m = testModel(80, 24)
	if cmd := press(m, tea.KeyCtrlD); cmd == nil || !m.closing {
		t.Fatal("empty Ctrl+D must exit")
	}
}

func TestStreamingUpsertFlushAndScroll(t *testing.T) {
	m := testModel(80, 24)
	m.busy = true
	for i := 0; i < 35; i++ {
		m.state.Messages = append(m.state.Messages, Message{ID: fmt.Sprint(i), Role: "assistant", Text: fmt.Sprintf("History line %d", i)})
	}
	m.refreshTranscript()
	if !m.viewport.AtBottom() {
		t.Fatal("initial content must follow bottom")
	}
	m.viewport.GotoTop()
	_, cmd := m.Update(MessageMsg{ID: "stream", Role: "assistant", Text: "partial"})
	if cmd == nil || !m.flushPending {
		t.Fatal("stream must schedule its own flush")
	}
	_, cmd = m.Update(MessageMsg{ID: "stream", Role: "assistant", Text: "complete"})
	if cmd != nil {
		t.Fatal("pending frame should coalesce")
	}
	m.Update(flushMsg{})
	if len(m.state.Messages) != 36 || !m.busy || m.viewport.YOffset != 0 {
		t.Fatal("stream duplicated, ended operation, or stole scroll")
	}
	m.viewport.GotoBottom()
	m.Update(MessageMsg{ID: "stream", Role: "assistant", Text: strings.Repeat("more\n", 20)})
	m.Update(flushMsg{})
	if !m.viewport.AtBottom() {
		t.Fatal("bottom should track streaming")
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 15})
	if !m.viewport.AtBottom() {
		t.Fatal("resize lost bottom tracking")
	}
	m.Update(OperationDoneMsg{})
	if m.busy {
		t.Fatal("operation completion did not unlock input")
	}
}

func TestSnapshotIsolationAndFooter(t *testing.T) {
	s := testState()
	m := New(s, Handlers{})
	s.Models[0].Levels[0] = "changed"
	if m.state.Models[0].Levels[0] == "changed" {
		t.Fatal("snapshot aliases nested slices")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if !strings.Contains(ansi.Strip(m.footer()), "(provider-0)") {
		t.Fatal("wide footer should include provider")
	}
	s = testState()
	s.ModelKey, s.Thinking, s.ContextKnown = s.Models[3].Key(), "low", false
	m.Update(OperationDoneMsg{Snapshot: &s})
	footer := ansi.Strip(m.footer())
	if !strings.Contains(footer, "?/1.0M") || !strings.Contains(footer, "model-03 • low") {
		t.Fatalf("snapshot footer = %q", footer)
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 15})
	if strings.Contains(ansi.Strip(m.footer()), "(provider-") {
		t.Fatal("narrow footer should omit provider")
	}
}

func TestProgramStreamingAndExit(t *testing.T) {
	tm := teatest.NewTestModel(t, New(testState(), Handlers{}), teatest.WithInitialTermSize(80, 24))
	tm.Send(MessageMsg{ID: "a", Role: "assistant", Text: "streamed response"})
	tm.Send(OperationDoneMsg{})
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlD})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*Model)
	if len(final.state.Messages) != 1 || final.state.Messages[0].Text != "streamed response" || !final.closing {
		t.Fatal("program lost stream or failed to exit")
	}
	output, err := io.ReadAll(tm.FinalOutput(t))
	if err != nil || !strings.Contains(ansi.Strip(string(output)), "streamed response") {
		t.Fatalf("final render missing stream: %v", err)
	}
}

func TestEditorUsesSinglePromptAndFixedDock(t *testing.T) {
	m := testModel(40, 16)
	typeText(m, "first\nsecond\nthird")
	editorLines := strings.Split(strings.TrimSuffix(m.editor.View(), "\n"), "\n")
	if len(editorLines) < 3 {
		t.Fatalf("editor lines = %d, want multiline view", len(editorLines))
	}
	plain := ansi.Strip(strings.Join(editorLines, "\n"))
	if strings.Count(plain, "❯ ") != 1 {
		t.Fatalf("prompt repeated in multiline input: %q", plain)
	}
	for _, line := range editorLines[1:] {
		if strings.HasPrefix(ansi.Strip(line), "❯ ") {
			t.Fatalf("continuation line has a new prompt: %q", line)
		}
	}
	footerBefore := strings.Join(strings.Split(m.View(), "\n")[m.height-2:], "\n")
	typeText(m, strings.Repeat("x\n", 20))
	footerAfter := strings.Join(strings.Split(m.View(), "\n")[m.height-2:], "\n")
	if footerBefore != footerAfter {
		t.Fatalf("footer moved while editor grew:\nbefore=%q\nafter=%q", footerBefore, footerAfter)
	}
}

func TestFrameFitsVerySmallTerminal(t *testing.T) {
	for _, height := range []int{1, 2, 3} {
		m := testModel(20, height)
		lines := strings.Split(m.View(), "\n")
		if len(lines) != height {
			t.Fatalf("height %d rendered %d lines", height, len(lines))
		}
	}
}

func TestTerminalMouseAndClipboardInput(t *testing.T) {
	m := testModel(60, 18)
	for i := 0; i < 30; i++ {
		m.state.Messages = append(m.state.Messages, Message{ID: fmt.Sprint(i), Role: "assistant", Text: "scrollable line"})
	}
	m.refreshTranscript()
	m.viewport.GotoBottom()
	before := m.viewport.YOffset
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[<64;2;3M")})
	if m.editor.Value() != "" || m.viewport.YOffset >= before {
		t.Fatalf("raw mouse report leaked or failed to scroll: value=%q offset=%d before=%d", m.editor.Value(), m.viewport.YOffset, before)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[<65;2;3M")})
	if m.editor.Value() != "" {
		t.Fatal("raw mouse report changed editor text")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;2;3M[<64;2;4M")})
	if m.editor.Value() != "" {
		t.Fatal("batched raw mouse reports changed editor text")
	}

	var copied string
	m.handlers.WriteClipboard = func(value string) error { copied = value; return nil }
	m.editor.SetValue("hello")
	m.layout()
	y := m.viewport.Height + 1
	m.Update(tea.MouseMsg{X: 3, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	cmd := func() tea.Msg {
		_, command := m.Update(tea.MouseMsg{X: 6, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
		if command == nil {
			return nil
		}
		return command()
	}()
	if cmd != nil {
		m.Update(cmd)
	}
	if copied != "ell" {
		t.Fatalf("selected text = %q, want %q", copied, "ell")
	}

	m.handlers.ReadClipboard = func() (string, error) { return "paste", nil }
	_, paste := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if paste == nil {
		t.Fatal("Ctrl+V did not request clipboard")
	}
	m.Update(paste())
	if !strings.Contains(m.editor.Value(), "paste") {
		t.Fatalf("clipboard paste missing: %q", m.editor.Value())
	}
}

func TestDoubleClickCopiesWord(t *testing.T) {
	m := testModel(60, 18)
	m.busy, m.status = true, "Working"
	m.editor.SetValue("hello world")
	m.layout()
	var copied string
	m.handlers.WriteClipboard = func(value string) error { copied = value; return nil }
	now := time.Unix(1000, 0)
	m.clock = func() time.Time { return now }
	y := m.viewport.Height + 1
	m.Update(tea.MouseMsg{X: 3, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	now = now.Add(100 * time.Millisecond)
	_, cmd := m.Update(tea.MouseMsg{X: 3, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if cmd == nil {
		t.Fatal("double click did not copy")
	}
	m.Update(cmd())
	if copied != "hello" || m.editor.Value() != "hello world" {
		t.Fatalf("double click copied %q or changed input to %q", copied, m.editor.Value())
	}
	border := ansi.Strip(m.frameLines()[m.viewport.Height])
	if !strings.HasSuffix(border, " copied! ──") || !strings.Contains(border, "Working") {
		t.Fatalf("copy confirmation must be on the right beside working status: %q", border)
	}
	assertFrame(t, m)
	m.Update(clearCopyNoticeMsg(m.copyNoticeSeq))
	if strings.Contains(ansi.Strip(m.View()), "copied!") || m.status != "Working" {
		t.Fatal("clearing copy confirmation changed the operation status")
	}
}
