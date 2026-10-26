package tui

import (
	"fmt"
	"io"
	"os"
	"reflect"
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

func TestInitialPromptDispatchesOnce(t *testing.T) {
	var requests []Request
	m := NewWithInitialPrompt(Snapshot{}, Handlers{Request: func(request Request) tea.Cmd {
		requests = append(requests, request)
		return nil
	}}, "next prompt")
	_ = m.Init()
	_ = m.Init()
	if len(requests) != 1 || requests[0].Kind != "run" || requests[0].Value != "next prompt" {
		t.Fatalf("initial requests = %+v, want one run request", requests)
	}
	if !m.busy {
		t.Fatal("initial prompt did not mark the operation busy")
	}
}

func TestMarkdownRendererBoundsWidthAndRoleCache(t *testing.T) {
	var renderer markdownRenderer
	for _, width := range []int{40, 80, 120, 40, 100, 140, 160, 180} {
		_ = renderer.render("**answer**", "assistant", width)
		_ = renderer.render("thinking", "thinking", width)
	}
	if len(renderer.widths) > 4 || len(renderer.renderers) > 8 {
		t.Fatalf("markdown renderer cache grew without bound: widths=%d renderers=%d", len(renderer.widths), len(renderer.renderers))
	}
}

func TestSelectorTinyBudgetKeepsFixedUI(t *testing.T) {
	m := testModel(80, 8)
	m.openSelector("model", "")
	for _, budget := range []int{3, 6} {
		view := m.selector.view(80, budget)
		lines := strings.Split(view, "\n")
		if len(lines) != m.selector.height(budget) {
			t.Fatalf("budget=%d rendered=%d measured=%d", budget, len(lines), m.selector.height(budget))
		}
		plain := ansi.Strip(view)
		if !strings.Contains(plain, "Select Model") || !strings.Contains(plain, "> ") {
			t.Fatalf("budget=%d lost selector fixed UI: %q", budget, plain)
		}
	}
}

// 工具行的点击命中按消息缓存：同一帧内重复渲染不改变命中数量
func TestToolHitsAreStableAcrossRenders(t *testing.T) {
	m := testModel(80, 24)
	m.busy = true
	group := &ToolGroupView{ID: "group", Calls: []ToolCallView{{ID: "call", Name: "bash", Arguments: `{"command":"echo hi"}`, Result: "hi", Status: ToolSuccess, ArgsComplete: true}}}
	m.state.Messages = []Message{{ID: "tools", Role: "assistant", ToolGroup: group}}
	m.refreshTranscript()
	first := len(m.lastToolHits)
	if first != 1 {
		t.Fatalf("hits = %d, want 1", first)
	}
	m.refreshTranscript()
	if len(m.lastToolHits) != first || m.lastToolHits[0] != m.cache["tools"].hits[0] {
		t.Fatalf("hits changed between renders: %+v", m.lastToolHits)
	}
}

func testState() Snapshot {
	s := Snapshot{Cwd: "/project/Rivo", Branch: "feat(tui)/fullscreen-tui", Thinking: "high"}
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
	dock := 1 + m.pendingRows() + m.editor.Height() + 2 + m.completionRows(len(m.completionItems())) + m.subagentRows()
	if m.selector != nil {
		selectorRows := len(strings.Split(m.selector.view(m.width, m.height-2), "\n"))
		dock = 1 + selectorRows + m.subagentRows()
		if selectorRows != m.selector.height(m.height-2) {
			t.Fatalf("selector measured %d, rendered %d", m.selector.height(m.height-2), selectorRows)
		}
	} else {
		// 输入态在视口与输入框之间固定留一行空白
		dock++
	}
	if got := m.viewport.Height + dock; got != m.height {
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
	statusIndex := len(lines) - 1 - m.subagentRows()
	if statusIndex < 0 || !strings.Contains(ansi.Strip(lines[statusIndex]), "/project/Rivo") {
		t.Fatal("cwd status bar moved or disappeared")
	}
}

func TestLayout(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {80, 24}, {60, 15}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := testModel(size[0], size[1])
			m.state.Messages = []Message{{ID: "u", Role: "user", Text: strings.Repeat("中文 👋 **bold** ", 30)}, {ID: "a", Role: "assistant", Text: "```go\n" + strings.Repeat("long_code", 40)}}
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
			if m.completionRows(len(m.completionItems())) != 5 {
				t.Fatal("slash must show five command rows")
			}
			for _, kind := range []RequestKind{RequestModel, RequestThinking, RequestResume} {
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

func TestBusySubmitRoutesPlainTextToSteering(t *testing.T) {
	m := testModel(60, 15)
	var request Request
	m.handlers.Request = func(got Request) tea.Cmd {
		request = got
		return nil
	}
	m.busy, m.status = true, "Working"
	typeText(m, "steer this run")
	press(m, tea.KeyEnter)
	if request.Kind != "steer" || request.Value != "steer this run" {
		t.Fatalf("request = %+v, want steering request", request)
	}
	if m.editor.Value() != "" || !m.busy {
		t.Fatal("steering submission cleared busy state incorrectly")
	}
}

func TestBusyTabRoutesPlainTextToFollowUp(t *testing.T) {
	m := testModel(60, 15)
	var request Request
	m.handlers.Request = func(got Request) tea.Cmd {
		request = got
		return nil
	}
	m.busy, m.status = true, "Working"
	typeText(m, "follow up this run")
	press(m, tea.KeyTab)
	if request.Kind != "follow-up" || request.Value != "follow up this run" {
		t.Fatalf("request = %+v, want follow-up request", request)
	}
	if m.editor.Value() != "" || !m.busy {
		t.Fatal("follow-up submission cleared busy state incorrectly")
	}
}

func TestBusyFollowUpControlCommandClearsQueue(t *testing.T) {
	m := testModel(60, 15)
	m.busy, m.status = true, "Working"
	m.state.FollowUp = []string{"follow-up"}
	m.state.FollowUpInputs = []ComposerPayload{textPayload("follow-up")}
	typeText(m, "/clear-follow-up")
	press(m, tea.KeyEnter)
	if len(m.state.FollowUp) != 0 || len(m.state.FollowUpInputs) != 0 {
		t.Fatalf("follow-up state = %v/%v, want empty", m.state.FollowUp, m.state.FollowUpInputs)
	}
	if !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "Cleared 1 follow-up message") {
		t.Fatalf("clear notice = %+v", m.state.Messages[len(m.state.Messages)-1])
	}
}

func TestCompactionStagesEnterAndTabThenFlushesByQueue(t *testing.T) {
	m := testModel(60, 15)
	var routed []string
	m.handlers.EnqueueSteeringMessage = func(message ComposerPayload) error {
		routed = append(routed, "steering:"+payloadText(message))
		return nil
	}
	m.handlers.EnqueueFollowUpMessage = func(message ComposerPayload) error {
		routed = append(routed, "follow-up:"+payloadText(message))
		return nil
	}
	m.busy, m.compacting = true, true
	m.operationIsRun = true
	typeText(m, "steer during compact")
	press(m, tea.KeyEnter)
	typeText(m, "follow during compact")
	press(m, tea.KeyTab)
	if len(m.compactionQueuedMessages) != 2 || len(routed) != 0 {
		t.Fatalf("staged = %+v, routed = %v", m.compactionQueuedMessages, routed)
	}
	m.Update(StatusMsg("Working"))
	if len(m.compactionQueuedMessages) != 0 {
		t.Fatal("compaction staged messages were not cleared after flush")
	}
	if !reflect.DeepEqual(routed, []string{"steering:steer during compact", "follow-up:follow during compact"}) {
		t.Fatalf("routed = %v", routed)
	}
	if !reflect.DeepEqual(m.state.Steering, []string{"steer during compact"}) || !reflect.DeepEqual(m.state.FollowUp, []string{"follow during compact"}) {
		t.Fatalf("queue mirror = %v/%v", m.state.Steering, m.state.FollowUp)
	}
}

// 回填图片 follow-up 后必须仍可组装出原图片，验证 marker 与 images 表同步
func TestRestoreFollowUpKeepsImageAttached(t *testing.T) {
	m := testModel(60, 15)
	queued := ComposerPayload{Text: "look", Images: []ImagePart{{Data: "aGk=", MIMEType: "image/png"}}}
	m.handlers.RestoreFollowUpMessages = func() []ComposerPayload { return []ComposerPayload{queued} }
	m.busy = true
	if restored := m.restoreQueuedFollowUp(); restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	built, ok := m.buildPayload()
	if !ok {
		t.Fatal("restored editor produced no payload")
	}
	if built.Text != "look" || len(built.Images) != 1 {
		t.Fatalf("payload = %+v, want text look with one image", built)
	}
	if built.Images[0] != (ImagePart{Data: "aGk=", MIMEType: "image/png"}) {
		t.Fatalf("image = %+v, want original attachment", built.Images[0])
	}
}

func TestCompactionFailureRestoresStagedInput(t *testing.T) {
	m := testModel(60, 15)
	m.handlers.EnqueueSteeringMessage = func(ComposerPayload) error { return fmt.Errorf("queue unavailable") }
	m.busy, m.compacting = true, true
	typeText(m, "restore after compact failure")
	press(m, tea.KeyEnter)
	m.Update(StatusMsg("Compaction failed"))
	if got := m.editor.Value(); got != "restore after compact failure" {
		t.Fatalf("editor = %q, want staged input restored", got)
	}
	if len(m.compactionQueuedMessages) != 0 {
		t.Fatal("staged input retained after restoration")
	}
}
func TestAltUpRestoresSteeringWithoutResubmitting(t *testing.T) {
	m := testModel(80, 24)
	var requests []Request
	var restored int
	m.handlers.Request = func(request Request) tea.Cmd {
		requests = append(requests, request)
		return nil
	}
	m.handlers.RestoreSteering = func() []string {
		restored++
		return []string{"queued message"}
	}
	m.busy, m.status = true, "Working"
	typeText(m, "draft")
	m.Update(tea.KeyMsg{Type: tea.KeyShiftLeft})
	if restored != 1 {
		t.Fatalf("restore calls = %d, want one call", restored)
	}
	if got, want := m.editor.Value(), "queued message\n\ndraft"; got != want {
		t.Fatalf("editor = %q, want %q", got, want)
	}
	if len(requests) != 0 {
		t.Fatalf("requests = %+v, want no resubmission", requests)
	}
	if m.state.Steering != nil {
		t.Fatalf("steering state = %+v, want empty", m.state.Steering)
	}
	if strings.Contains(m.status, "Restored") {
		t.Fatalf("restore status = %q, want no success message", m.status)
	}
}

func TestAltUpDoesNotDuplicateSubmittedSteering(t *testing.T) {
	m := testModel(80, 24)
	queued := make([]string, 0, 1)
	m.handlers.EnqueueSteering = func(value string) error {
		queued = append(queued, value)
		return nil
	}
	m.handlers.RestoreSteering = func() []string {
		messages := append([]string(nil), queued...)
		queued = nil
		return messages
	}
	m.busy, m.status = true, "Working"
	typeText(m, "same message")
	press(m, tea.KeyEnter)
	m.Update(tea.KeyMsg{Type: tea.KeyShiftLeft})
	if len(queued) != 0 {
		t.Fatalf("backend queue = %+v, want empty after dequeue", queued)
	}
	if got, want := m.editor.Value(), "same message"; got != want {
		t.Fatalf("editor = %q, want one restored message", got)
	}
	if strings.Count(m.editor.Value(), "same message") != 1 {
		t.Fatalf("restored message appears more than once: %q", m.editor.Value())
	}
}

func TestOperationDonePreservesSteeringWithdrawalAgainstStaleSnapshot(t *testing.T) {
	m := testModel(80, 24)
	m.busy = true
	m.state.Steering = []string{"queued"}
	m.handlers.RestoreSteering = func() []string { return []string{"queued"} }
	m.Update(tea.KeyMsg{Type: tea.KeyShiftLeft})
	if got := m.editor.Value(); got != "queued" {
		t.Fatalf("editor = %q, want queued message", got)
	}
	if len(m.state.Steering) != 0 {
		t.Fatalf("steering = %+v, want empty", m.state.Steering)
	}
	m.Update(OperationDoneMsg{Snapshot: &Snapshot{Steering: []string{"queued"}}, RetainMessages: true})
	if len(m.state.Steering) != 0 {
		t.Fatalf("stale snapshot restored steering = %+v, want empty", m.state.Steering)
	}
}

func TestPendingSteeringViewMatchesPiShape(t *testing.T) {
	m := testModel(80, 24)
	m.busy = true
	m.state.Steering = []string{"first", "second"}
	m.layout()
	plain := ansi.Strip(m.View())
	for _, want := range []string{"Steering: first", "Steering: second", "↳ " + steeringDequeueKeyLabel() + " to edit steering"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("view = %q, missing %q", plain, want)
		}
	}
	assertFrame(t, m)
}

// 队列撤回用横向键,与任务列表的 Up/Down 选择互不冲突
func TestDequeueKeyLabels(t *testing.T) {
	if got := steeringDequeueKeyLabel(); got != "shift+left" {
		t.Fatalf("steering label = %q", got)
	}
	if got := followUpDequeueKeyLabel(); got != "shift+right" {
		t.Fatalf("follow-up label = %q", got)
	}
}

func TestShiftRightRestoresFollowUpWithoutTouchingSteering(t *testing.T) {
	m := testModel(80, 24)
	restored := 0
	m.handlers.RestoreFollowUpMessages = func() []ComposerPayload {
		restored++
		return []ComposerPayload{textPayload("queued follow-up")}
	}
	m.busy, m.status = true, "Working"
	m.state.Steering = []string{"keep steering"}
	typeText(m, "draft")
	m.Update(tea.KeyMsg{Type: tea.KeyShiftRight})
	if restored != 1 {
		t.Fatalf("restore calls = %d, want one call", restored)
	}
	if got, want := m.editor.Value(), "queued follow-up\n\ndraft"; got != want {
		t.Fatalf("editor = %q, want %q", got, want)
	}
	if len(m.state.Steering) != 1 || m.state.Steering[0] != "keep steering" {
		t.Fatalf("steering = %v, want untouched", m.state.Steering)
	}
}

func TestFollowUpDequeueKeyVariants(t *testing.T) {
	for _, test := range []struct {
		name string
		key  tea.KeyMsg
		want bool
	}{
		{name: "shift-right", key: tea.KeyMsg{Type: tea.KeyShiftRight}, want: true},
		{name: "shift-left", key: tea.KeyMsg{Type: tea.KeyShiftLeft}, want: false},
		{name: "plain-right", key: tea.KeyMsg{Type: tea.KeyRight}, want: false},
		{name: "alt-right", key: tea.KeyMsg{Type: tea.KeyRight, Alt: true}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isFollowUpDequeueKey(test.key); got != test.want {
				t.Fatalf("isFollowUpDequeueKey(%q) = %v, want %v", test.key.String(), got, test.want)
			}
		})
	}
}

func TestPlainDownWithoutFollowUpNavigatesHistory(t *testing.T) {
	m := testModel(80, 24)
	restored := 0
	m.handlers.RestoreFollowUpMessages = func() []ComposerPayload {
		restored++
		return []ComposerPayload{textPayload("queued")}
	}
	typeText(m, "draft")
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.editor.Value(); got != "draft" {
		t.Fatalf("editor = %q, want draft after history navigation", got)
	}
	if restored != 0 {
		t.Fatalf("restore calls = %d, want none without visible follow-up", restored)
	}
}

func TestPendingSteeringViewUsesOnlyTheFirstLine(t *testing.T) {
	m := testModel(80, 24)
	m.state.Steering = []string{"first line\nsecond line"}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "Steering: first line") {
		t.Fatalf("view = %q, missing first line", plain)
	}
	if strings.Contains(plain, "second line") {
		t.Fatalf("view = %q, rendered text after first line", plain)
	}
}

func TestEscapeRestoresSteeringBeforeCancel(t *testing.T) {
	m := testModel(80, 24)
	cancelled := 0
	m.handlers.RestoreSteering = func() []string { return []string{"queued message"} }
	m.handlers.Cancel = func() { cancelled++ }
	m.busy = true
	press(m, tea.KeyEsc)
	if cancelled != 1 {
		t.Fatalf("cancelled = %d, want one cancellation", cancelled)
	}
	if got, want := m.editor.Value(), "queued message"; got != want {
		t.Fatalf("editor = %q, want %q", got, want)
	}
}

func TestRejectedSteeringRestoresInputAfterOperationEnds(t *testing.T) {
	m := testModel(60, 15)
	m.handlers.Request = func(Request) tea.Cmd { return nil }
	m.busy, m.status = true, "Working"
	typeText(m, "late steering")
	press(m, tea.KeyEnter)
	m.Update(OperationDoneMsg{ID: m.operationID})
	m.Update(SteeringResultMsg{ID: m.operationID, Value: "late steering", Err: fmt.Errorf("operation ended")})
	if m.editor.Value() != "late steering" {
		t.Fatalf("restored editor = %q, want late steering", m.editor.Value())
	}
}

func TestRejectedSteeringDoesNotOverwriteNewDraft(t *testing.T) {
	m := testModel(60, 15)
	m.busy = true
	typeText(m, "new draft")
	m.Update(SteeringResultMsg{Value: "late steering", Err: fmt.Errorf("operation ended")})
	if got := m.editor.Value(); got != "new draft" {
		t.Fatalf("draft = %q, want new draft", got)
	}
}

func TestSteeringSnapshotsIgnoreOutOfOrderUpdates(t *testing.T) {
	m := testModel(60, 15)
	m.busy = true
	m.Update(SteeringResultMsg{Value: "new", Revision: 2})
	m.Update(SteeringStateMsg{Messages: nil, Revision: 1})
	if got := m.state.Steering; len(got) != 1 || got[0] != "new" {
		t.Fatalf("stale snapshot cleared steering = %v", got)
	}

	m.Update(SteeringStateMsg{Messages: nil, Revision: 3})
	m.Update(SteeringResultMsg{Value: "stale", Revision: 2})
	if len(m.state.Steering) != 0 {
		t.Fatalf("stale result appended steering = %v", m.state.Steering)
	}
}

func TestEmptySteeringRestoreKeepsSnapshotWithoutReplayingIt(t *testing.T) {
	m := testModel(60, 15)
	m.busy = true
	m.state.Steering = []string{"already consumed"}
	m.state.SteeringInputs = []ComposerPayload{{Text: "already consumed"}}
	m.handlers.RestoreSteeringMessages = func() []ComposerPayload { return nil }
	m.Update(tea.KeyMsg{Type: tea.KeyShiftLeft})
	if got := m.editor.Value(); got != "" {
		t.Fatalf("editor = %q, want no stale replay", got)
	}
	if len(m.state.Steering) != 1 || m.state.Steering[0] != "already consumed" {
		t.Fatalf("steering snapshot = %v, want preserved snapshot", m.state.Steering)
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
	typeText(m, "/eff")
	press(m, tea.KeyEnter)
	if m.selector == nil || m.selector.kind != "thinking" {
		t.Fatal("Enter must complete and execute")
	}
}

func TestNonTreeSelectorForwardsHorizontalCursorKeys(t *testing.T) {
	m := testModel(80, 24)
	m.openSelector("model", "")
	typeText(m, "abc")
	press(m, tea.KeyLeft)
	typeText(m, "X")
	if got, want := m.selector.input.Value(), "abXc"; got != want {
		t.Fatalf("left cursor movement = %q, want %q", got, want)
	}
	press(m, tea.KeyRight)
	typeText(m, "Y")
	if got, want := m.selector.input.Value(), "abXcY"; got != want {
		t.Fatalf("right cursor movement = %q, want %q", got, want)
	}
}

func TestTreeSelectorUsesUserNodesAndRestoresEditor(t *testing.T) {
	m := testModel(100, 30)
	m.state.Tree = []TreeNode{
		{ID: "u1", Text: "first", Depth: 0},
		{ID: "u2", ParentID: "u1", Prefix: "├─ ", Text: "second line", Depth: 1, Current: true},
		{ID: "child", ParentID: "u2", Prefix: "│  └─ ", Text: "nested child", Depth: 2},
		{ID: "u3", ParentID: "u1", Prefix: "└─ ", Text: "third line", Depth: 1},
		{ID: "child-3", ParentID: "u3", Prefix: "   └─ ", Text: "last child", Depth: 2},
	}
	var requests []Request
	m.handlers.Request = func(request Request) tea.Cmd {
		requests = append(requests, request)
		return nil
	}
	m.openSelector("tree", "")
	if m.selector == nil || m.selector.title != "Conversation tree" || len(m.selector.filtered) != 5 {
		t.Fatalf("tree selector = %+v", m.selector)
	}
	if !strings.Contains(m.selector.filtered[1].label, "├─ second line") {
		t.Fatalf("tree connector missing: %q", m.selector.filtered[1].label)
	}
	view := ansi.Strip(m.selector.view(m.width, m.height-2))
	if strings.Count(view, "second line") != 1 {
		t.Fatalf("selected tree text repeated in selector: %q", view)
	}
	if got, want := len(strings.Split(view, "\n")), m.selector.height(m.height-2); got != want {
		t.Fatalf("tree selector height = %d, want %d", got, want)
	}
	press(m, tea.KeyUp)
	if got := m.selector.filtered[m.selector.selected].value; got != "u1" {
		t.Fatalf("first sibling up navigation = %q, want root u1", got)
	}
	press(m, tea.KeyRight)
	if got := m.selector.filtered[m.selector.selected].value; got != "u2" {
		t.Fatalf("root right navigation = %q, want first child u2", got)
	}
	press(m, tea.KeyDown)
	if got := m.selector.filtered[m.selector.selected].value; got != "u3" {
		t.Fatalf("same-level navigation = %q, want sibling u3", got)
	}
	press(m, tea.KeyRight)
	if got := m.selector.filtered[m.selector.selected].value; got != "child-3" {
		t.Fatalf("last sibling right navigation = %q, want child child-3", got)
	}
	press(m, tea.KeyLeft)
	press(m, tea.KeyDown)
	if got := m.selector.filtered[m.selector.selected].value; got != "child-3" {
		t.Fatalf("last sibling down navigation = %q, want child child-3", got)
	}
	press(m, tea.KeyLeft)
	if got := m.selector.filtered[m.selector.selected].value; got != "u3" {
		t.Fatalf("left navigation = %q, want parent u3", got)
	}
	press(m, tea.KeyUp)
	if got := m.selector.filtered[m.selector.selected].value; got != "u2" {
		t.Fatalf("sibling up navigation = %q, want first child u2", got)
	}
	press(m, tea.KeyRight)
	if got := m.selector.filtered[m.selector.selected].value; got != "child" {
		t.Fatalf("right child navigation = %q, want child", got)
	}
	press(m, tea.KeyLeft)
	press(m, tea.KeyUp)
	if got := m.selector.filtered[m.selector.selected].value; got != "u1" {
		t.Fatalf("root parent navigation = %q, want root u1", got)
	}
	press(m, tea.KeyUp)
	if got := m.selector.filtered[m.selector.selected].value; got != "u3" {
		t.Fatalf("root up navigation = %q, want last child u3", got)
	}
	press(m, tea.KeyDown)
	if got := m.selector.filtered[m.selector.selected].value; got != "child-3" {
		t.Fatalf("last child down navigation = %q, want nested child child-3", got)
	}
	press(m, tea.KeyLeft)
	if got := m.selector.filtered[m.selector.selected].value; got != "u3" {
		t.Fatalf("child parent navigation = %q, want u3", got)
	}
	press(m, tea.KeyLeft)
	if got := m.selector.filtered[m.selector.selected].value; got != "u1" {
		t.Fatalf("root left navigation = %q, want root u1", got)
	}
	press(m, tea.KeyDown)
	if got := m.selector.filtered[m.selector.selected].value; got != "u2" {
		t.Fatalf("root down navigation = %q, want first child u2", got)
	}
	press(m, tea.KeyLeft)
	press(m, tea.KeyLeft)
	if got := m.selector.filtered[m.selector.selected].value; got != "u3" {
		t.Fatalf("root left navigation = %q, want last child u3", got)
	}
	press(m, tea.KeyLeft)
	if got := m.selector.filtered[m.selector.selected].value; got != "u1" {
		t.Fatalf("last child parent navigation = %q, want root u1", got)
	}
	press(m, tea.KeyEnter)
	if len(requests) != 0 || m.selector == nil || m.selector.kind != "tree_restore" || m.selector.treeRestoreTarget != "u1" || m.selector.filtered[0].label != "Restore conversation" {
		t.Fatalf("tree request = %+v selector=%v", requests, m.selector)
	}
	press(m, tea.KeyEnter)
	if len(requests) != 1 || requests[0].Kind != "tree" || requests[0].Value != "u1" || requests[0].RestoreMode != "context" || m.selector != nil {
		t.Fatalf("tree request = %+v selector=%v", requests, m.selector)
	}
	m.Update(OperationDoneMsg{Snapshot: &Snapshot{Tree: m.state.Tree}, RestoreEditor: true, EditorValue: "first"})
	if m.editor.Value() != "first" {
		t.Fatalf("editor = %q, want selected root text", m.editor.Value())
	}
}

func TestTreeSelectorRejectsBusyOperation(t *testing.T) {
	m := testModel(80, 24)
	m.busy = true
	m.openSelector("tree", "")
	if m.selector != nil {
		t.Fatal("tree selector opened while busy")
	}
	if len(m.state.Messages) == 0 || !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "Wait for the current operation") {
		t.Fatalf("busy notice = %+v", m.state.Messages)
	}
	m = testModel(80, 24)
	m.busy = true
	typeText(m, "/tree")
	press(m, tea.KeyEnter)
	if len(m.state.Messages) == 0 || !strings.Contains(m.state.Messages[len(m.state.Messages)-1].Text, "Wait for the current operation") {
		t.Fatalf("busy submit notice = %+v", m.state.Messages)
	}
}

func TestTreeRestoreSelectorDispatchesCodeMode(t *testing.T) {
	m := testModel(100, 30)
	m.state.Tree = []TreeNode{{ID: "u1", Text: "first", Current: true}}
	var requests []Request
	m.handlers.Request = func(request Request) tea.Cmd {
		requests = append(requests, request)
		return nil
	}
	m.openSelector("tree", "")
	press(m, tea.KeyEnter)
	if m.selector == nil || m.selector.kind != "tree_restore" || len(m.selector.filtered) != 3 || m.selector.filtered[2].label != "Restore code and conversation" {
		t.Fatalf("restore selector = %+v, want three choices", m.selector)
	}
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if len(requests) != 1 || requests[0].Value != "u1" || requests[0].RestoreMode != "code" {
		t.Fatalf("requests = %+v, want code restore for u1", requests)
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

func TestFooterSingleLine(t *testing.T) {
	m := testModel(120, 30)
	footer := ansi.Strip(m.footer())
	if strings.Count(footer, "\n") != 0 {
		t.Fatalf("footer should be a single line: %q", footer)
	}
	if ansi.StringWidth(footer) != 120 {
		t.Fatalf("footer width = %d, want 120 (full-width aligned): %q", ansi.StringWidth(footer), footer)
	}
	if !strings.HasPrefix(footer, "/project/Rivo • \uf418 feat(tui)/fullscreen-tui") {
		t.Fatalf("footer should start with cwd • icon branch: %q", footer)
	}
	if !strings.HasSuffix(footer, "model-00 • high") {
		t.Fatalf("footer should end with model and thinking: %q", footer)
	}
}

func TestFooterWithoutBranch(t *testing.T) {
	m := testModel(120, 30)
	m.state.Branch = ""
	footer := ansi.Strip(m.footer())
	if !strings.HasPrefix(footer, "/project/Rivo ") || strings.Contains(footer, "\uf418") || strings.Contains(footer, "Rivo •") {
		t.Fatalf("empty branch should drop the separator and icon: %q", footer)
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
	if strings.Contains(ansi.Strip(m.footer()), "(provider-") {
		t.Fatalf("footer should never show the provider: %q", ansi.Strip(m.footer()))
	}
	s = testState()
	s.ModelKey, s.Thinking = s.Models[3].Key(), "low"
	m.Update(OperationDoneMsg{Snapshot: &s})
	if !strings.Contains(ansi.Strip(m.footer()), "model-03 • low") {
		t.Fatalf("snapshot footer = %q", ansi.Strip(m.footer()))
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 15})
	if strings.Contains(ansi.Strip(m.footer()), "provider-") {
		t.Fatal("narrow footer should still omit provider")
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
	footerBefore := strings.Join(strings.Split(m.View(), "\n")[m.height-1:], "\n")
	typeText(m, strings.Repeat("x\n", 20))
	footerAfter := strings.Join(strings.Split(m.View(), "\n")[m.height-1:], "\n")
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
	// 视口与输入框之间有一行空白，编辑器首行下移一行
	y := m.viewport.Height + 2
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
	y := m.viewport.Height + 2
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
	border := ansi.Strip(m.frameLines()[m.viewport.Height+1])
	if !strings.HasSuffix(border, " copied! ──") || !strings.Contains(border, "Working") {
		t.Fatalf("copy confirmation must be on the right beside working status: %q", border)
	}
	assertFrame(t, m)
	m.Update(clearCopyNoticeMsg(m.copyNoticeSeq))
	if strings.Contains(ansi.Strip(m.View()), "copied!") || m.status != "Working" {
		t.Fatal("clearing copy confirmation changed the operation status")
	}
}
