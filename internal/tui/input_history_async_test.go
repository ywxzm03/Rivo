package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// persistentHistoryModel 保留请求不执行 I/O，响应由用例按指定顺序交付
func persistentHistoryModel(count int, replay ...ComposerHistoryEntry) (*Model, *[]HistoryLookupRequest) {
	var requests []HistoryLookupRequest
	state := testState()
	state.InputHistory = &InputHistoryState{
		Metadata: InputHistoryMetadata{Generation: 11, SessionID: "main", PersistentCount: count},
		ScopeID:  "main", Replay: replay, ResetAll: true,
	}
	m := New(state, Handlers{History: HistoryHandlers{
		Lookup: func(request HistoryLookupRequest) tea.Cmd {
			requests = append(requests, request)
			return func() tea.Msg {
				return HistoryLookupMsg{Request: request, Text: fmt.Sprintf("stored-%d", request.Offset)}
			}
		},
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m, &requests
}

func latestHistoryRequest(t *testing.T, requests *[]HistoryLookupRequest, offset int) HistoryLookupRequest {
	t.Helper()
	if len(*requests) == 0 {
		t.Fatal("no persistent lookup request was issued")
	}
	request := (*requests)[len(*requests)-1]
	if request.Offset != offset || request.Generation != 11 || request.ScopeID != "main" {
		t.Fatalf("lookup = %+v, want generation 11 main offset %d", request, offset)
	}
	return request
}

func TestInputHistoryAsyncCannotOverwriteDraft(t *testing.T) {
	for _, edit := range []string{"typed text", "image", "queue withdrawal", "overlay", "new generation"} {
		t.Run(edit, func(t *testing.T) {
			m, requests := persistentHistoryModel(2)
			press(m, tea.KeyUp)
			request := latestHistoryRequest(t, requests, 1)
			switch edit {
			case "typed text":
				typeText(m, "fresh draft")
			case "image":
				m.Update(clipboardReadMsg{data: ClipboardData{Images: []ImagePart{{Data: "fresh-image", MIMEType: "image/png"}}}})
			case "queue withdrawal":
				m.handlers.RestoreSteeringMessages = func() []ComposerPayload { return []ComposerPayload{{Text: "withdrawn draft"}} }
				m.restoreQueuedMessages()
			case "overlay":
				m.openSelector(RequestModel, "")
			case "new generation":
				m.Update(OperationDoneMsg{InputHistory: &InputHistoryState{
					Metadata: InputHistoryMetadata{Generation: 12, SessionID: "other", PersistentCount: 2},
					ScopeID:  "other", ResetAll: true,
				}})
			}
			want := m.editor.Value()
			m.Update(HistoryLookupMsg{Request: request, Text: "late stored input"})
			assertHistoryText(t, m, want)
			if edit == "image" && m.composer.images[1].Data != "fresh-image" {
				t.Fatalf("late reply replaced fresh attachment: %v", m.composer.images)
			}
			if edit == "new generation" {
				press(m, tea.KeyUp)
				last := (*requests)[len(*requests)-1]
				if last.Generation != 12 || last.ScopeID != "other" || last.Offset != 1 {
					t.Fatalf("old generation was reused: %+v", last)
				}
				assertHistoryText(t, m, "")
			}
		})
	}
}

func TestInputHistoryCancelledLookupDoesNotSkip(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d pending requests", count), func(t *testing.T) {
			m, requests := persistentHistoryModel(3)
			for i := range count {
				press(m, tea.KeyUp)
				latestHistoryRequest(t, requests, 2-i)
			}
			typeText(m, "new draft")
			for _, request := range *requests {
				m.Update(HistoryLookupMsg{Request: request, Text: fmt.Sprintf("stored-%d", request.Offset)})
			}
			assertHistoryText(t, m, "new draft")
			eraseHistoryDraft(m)
			press(m, tea.KeyUp)
			assertHistoryText(t, m, "stored-2")
		})
	}
}

func TestInputHistoryAsyncLatestRequestWins(t *testing.T) {
	m, requests := persistentHistoryModel(3)
	press(m, tea.KeyUp)
	press(m, tea.KeyUp)
	press(m, tea.KeyDown)
	if len(*requests) != 3 {
		t.Fatalf("requests = %+v, want offsets 2, 1, 2", *requests)
	}
	for i, offset := range []int{2, 1, 2} {
		if (*requests)[i].Offset != offset {
			t.Fatalf("request %d offset = %d, want %d", i, (*requests)[i].Offset, offset)
		}
	}
	for _, index := range []int{2, 1, 0} {
		request := (*requests)[index]
		m.Update(HistoryLookupMsg{Request: request, Text: fmt.Sprintf("stored-%d", request.Offset)})
		assertHistoryText(t, m, "stored-2")
	}
	press(m, tea.KeyDown)
	assertHistoryText(t, m, "")
	for _, request := range *requests {
		m.Update(HistoryLookupMsg{Request: request, Text: fmt.Sprintf("stored-%d", request.Offset)})
	}
	assertHistoryText(t, m, "")
}

func TestInputHistoryCursorMoveAndLookupFailure(t *testing.T) {
	t.Run("cursor move cancels pending recall and restores anchor", func(t *testing.T) {
		local := ComposerHistoryEntry{Draft: ComposerDraft{Text: "first\nlast"}}
		m, requests := persistentHistoryModel(1, local)
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "first\nlast")
		press(m, tea.KeyUp)
		request := latestHistoryRequest(t, requests, 0)
		press(m, tea.KeyHome)
		m.Update(HistoryLookupMsg{Request: request, Text: "stored-0"})
		assertHistoryText(t, m, "first\nlast")
		if m.editor.LineInfo().StartColumn+m.editor.LineInfo().ColumnOffset != 0 {
			t.Fatal("late reply moved the user's cursor")
		}
		eraseHistoryDraft(m)
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "stored-0")
	})
	t.Run("read failure retries the same unseen position", func(t *testing.T) {
		m, requests := persistentHistoryModel(2)
		press(m, tea.KeyUp)
		request := latestHistoryRequest(t, requests, 1)
		m.Update(HistoryLookupMsg{Request: request, Err: errors.New("read unavailable")})
		assertHistoryText(t, m, "")
		press(m, tea.KeyUp)
		retry := latestHistoryRequest(t, requests, 1)
		m.Update(HistoryLookupMsg{Request: request, Text: "stored-1"})
		assertHistoryText(t, m, "")
		m.Update(HistoryLookupMsg{Request: retry, Text: "stored-1"})
		assertHistoryText(t, m, "stored-1")
	})
}

func TestInputHistoryReplaySkipsOnlyMatchingPersistentText(t *testing.T) {
	t.Run("replayed text is skipped in both directions", func(t *testing.T) {
		m, requests := persistentHistoryModel(3, ComposerHistoryEntry{Draft: ComposerDraft{Text: "replayed"}, Replayed: true})
		press(m, tea.KeyUp)
		assertHistoryText(t, m, "replayed")
		press(m, tea.KeyUp)
		m.Update(HistoryLookupMsg{Request: latestHistoryRequest(t, requests, 2), Text: "recent stored"})
		press(m, tea.KeyUp)
		m.Update(HistoryLookupMsg{Request: latestHistoryRequest(t, requests, 1), Text: "replayed"})
		m.Update(HistoryLookupMsg{Request: latestHistoryRequest(t, requests, 0), Text: "oldest stored"})
		assertHistoryText(t, m, "oldest stored")
		press(m, tea.KeyDown)
		assertHistoryText(t, m, "recent stored")
	})
	t.Run("ordinary local submission does not remove persistent duplicate", func(t *testing.T) {
		m, requests := persistentHistoryModel(1, ComposerHistoryEntry{Draft: ComposerDraft{Text: "same"}})
		press(m, tea.KeyUp)
		press(m, tea.KeyUp)
		m.Update(HistoryLookupMsg{Request: latestHistoryRequest(t, requests, 0), Text: "same"})
		assertHistoryText(t, m, "same")
		press(m, tea.KeyDown)
		assertHistoryText(t, m, "same")
		press(m, tea.KeyDown)
		assertHistoryText(t, m, "")
	})
}

func TestInputHistoryScopeNavigationIsolation(t *testing.T) {
	main := []ComposerHistoryEntry{{Draft: ComposerDraft{Text: "main-old"}}, {Draft: ComposerDraft{Text: "main-new"}}}
	m, _ := persistentHistoryModel(0, main...)
	tasks := []SubagentTaskView{{ID: "task-a", Status: "running"}, {ID: "task-b", Status: "running"}}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: tasks})
	var preparations []ScopePrepareRequest
	m.handlers.History.PrepareScope = func(request ScopePrepareRequest) tea.Cmd {
		preparations = append(preparations, request)
		return func() tea.Msg { return ScopePreparedMsg{Request: request} }
	}
	m.handlers.SubagentRequest = func(SubagentRequest) tea.Cmd { return nil }
	seeds := map[string][]ComposerHistoryEntry{
		"task-a": {{Draft: ComposerDraft{Text: "a-old"}}, {Draft: ComposerDraft{Text: "a-new"}}},
		"task-b": {{Draft: ComposerDraft{Text: "b-old"}}, {Draft: ComposerDraft{Text: "b-new"}}},
	}
	selectScope := func(id string) {
		t.Helper()
		eraseHistoryDraft(m)
		press(m, tea.KeyLeft)
		for i := 0; i < 3 && m.panel.selectedTaskID != id; i++ {
			press(m, tea.KeyDown)
		}
		before := len(preparations)
		press(m, tea.KeyEnter)
		if len(preparations) > before {
			request := preparations[len(preparations)-1]
			m.Update(ScopePreparedMsg{Request: request, Replay: seeds[id]})
		}
		if m.panel.viewingTaskID != id || !m.editor.Focused() {
			t.Fatalf("scope switch to %q: viewing=%q focused=%v", id, m.panel.viewingTaskID, m.editor.Focused())
		}
	}
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "main-new")
	selectScope("task-a")
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "a-new")
	selectScope("task-b")
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "b-new")
	selectScope("")
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "main-old")
	selectScope("task-a")
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "a-old")
}

func TestInputHistoryScopePreparationCanBeCancelled(t *testing.T) {
	m, _ := persistentHistoryModel(0)
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: []SubagentTaskView{{ID: "child", Status: "running"}}})
	var preparations []ScopePrepareRequest
	var entered []SubagentRequest
	m.handlers.History.PrepareScope = func(request ScopePrepareRequest) tea.Cmd {
		preparations = append(preparations, request)
		return func() tea.Msg { return ScopePreparedMsg{Request: request} }
	}
	m.handlers.SubagentRequest = func(request SubagentRequest) tea.Cmd { entered = append(entered, request); return nil }
	press(m, tea.KeyLeft)
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if len(preparations) != 1 || m.panel.viewingTaskID != "" || len(entered) != 0 {
		t.Fatalf("preparation changed visible/backend view early: preparations=%v view=%q entered=%v", preparations, m.panel.viewingTaskID, entered)
	}
	first := preparations[0]
	typeText(m, "stay in main")
	m.Update(ScopePreparedMsg{Request: first, Replay: []ComposerHistoryEntry{{Draft: ComposerDraft{Text: "child seed"}}}})
	assertHistoryText(t, m, "stay in main")
	if m.panel.viewingTaskID != "" || len(entered) != 0 {
		t.Fatal("cancelled preparation entered the backend child view")
	}
	eraseHistoryDraft(m)
	press(m, tea.KeyLeft)
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if len(preparations) != 2 {
		t.Fatalf("retry did not prepare scope: %v", preparations)
	}
	m.Update(ScopePreparedMsg{Request: preparations[1], Replay: []ComposerHistoryEntry{{Draft: ComposerDraft{Text: "child seed"}}}})
	if m.panel.viewingTaskID != "child" || len(entered) != 1 || entered[0].Kind != "enter" {
		t.Fatalf("valid preparation did not enter child: viewing=%q entered=%v", m.panel.viewingTaskID, entered)
	}
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "child seed")
}

func TestInputHistoryInitialPromptAndFreshOrigins(t *testing.T) {
	state := testState()
	state.InputHistory = &InputHistoryState{Metadata: InputHistoryMetadata{Generation: 11, SessionID: "main"}, ScopeID: "main", ResetAll: true}
	var requests []Request
	m := NewWithInitialPrompt(state, Handlers{Request: func(request Request) tea.Cmd { requests = append(requests, request); return nil }}, "initial prompt")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Init()
	m.Init()
	if len(requests) != 1 || requests[0].Payload == nil || requests[0].Payload.Origin == nil {
		t.Fatalf("initial prompt lost direct user origin: %+v", requests)
	}
	first := *requests[0].Payload.Origin
	if first.Generation != 11 || first.SessionID != "main" || first.Text != "initial prompt" {
		t.Fatalf("initial origin = %+v", first)
	}
	m.busy = false
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "initial prompt")
	press(m, tea.KeyEnter)
	if len(requests) != 2 || requests[1].Payload == nil || requests[1].Payload.Origin == nil {
		t.Fatalf("recalled submit lost user origin: %+v", requests)
	}
	second := *requests[1].Payload.Origin
	if second.Generation != first.Generation || second.SessionID != first.SessionID || second.Text != first.Text || second.Sequence == first.Sequence {
		t.Fatalf("manual resubmit reused identity: first=%+v second=%+v", first, second)
	}
}

func TestInputHistoryClearedOriginAndErrorAcknowledgement(t *testing.T) {
	m, _ := persistentHistoryModel(0)
	var recorded, acknowledged []InputOrigin
	failure := errors.New("history admission failed")
	m.handlers.History.RecordCleared = func(origin InputOrigin) tea.Cmd {
		recorded = append(recorded, origin)
		return func() tea.Msg { return HistoryRecordMsg{Origin: origin, Err: failure} }
	}
	m.handlers.History.Acknowledge = func(origin InputOrigin) { acknowledged = append(acknowledged, origin) }
	pasteText(m, strings.Repeat("pasted line\n", 12))
	want := m.editor.Value()
	cmd := press(m, tea.KeyCtrlC)
	assertHistoryText(t, m, "")
	if cmd == nil || len(recorded) != 1 || recorded[0].Text != want || recorded[0].SessionID != "main" || recorded[0].Generation != 11 {
		t.Fatalf("cleared origin = %+v, cmd nil=%v", recorded, cmd == nil)
	}
	// 同步入队失败也先返回 Cmd，错误展示后再确认原输入，不能在 Update 内回送自身
	m.Update(cmd())
	found := false
	for _, message := range m.state.Messages {
		found = found || strings.Contains(message.Text, failure.Error())
	}
	if !found || len(acknowledged) != 1 || acknowledged[0] != recorded[0] {
		t.Fatalf("history error not visibly acknowledged: found=%v acknowledged=%+v", found, acknowledged)
	}
}

func TestInputHistorySubagentEscapeAndRevision(t *testing.T) {
	m, _ := persistentHistoryModel(0, ComposerHistoryEntry{Draft: ComposerDraft{Text: "main only"}})
	tasks := []SubagentTaskView{{ID: "child", Status: "running"}}
	m.applySubagentSnapshot(SubagentSnapshot{Tasks: tasks})
	var controls []SubagentRequest
	m.handlers.SubagentRequest = func(request SubagentRequest) tea.Cmd { controls = append(controls, request); return nil }
	// 真实入口先安装准备好的种子，再进入 child 视图
	m.installInputHistory(&InputHistoryState{Metadata: InputHistoryMetadata{Generation: 11, SessionID: "main"}, ScopeID: "child", Replay: []ComposerHistoryEntry{{Draft: ComposerDraft{Text: "child only"}}}})
	m.enterSubagentView("child")
	m.Update(SubagentSnapshotMsg{
		Snapshot: SubagentSnapshot{Tasks: tasks, ViewingTaskID: "child", Revision: 8},
		InputHistory: &InputHistoryState{
			Metadata: InputHistoryMetadata{Generation: 11, SessionID: "main"}, ScopeID: "child",
			Replay: []ComposerHistoryEntry{{Draft: ComposerDraft{Text: "child only"}}},
		},
	})
	press(m, tea.KeyLeft)
	press(m, tea.KeyEsc)
	if m.panel.taskSelector || m.panel.viewingTaskID != "child" || !m.editor.Focused() || len(controls) != 0 {
		t.Fatalf("list Esc stopped or left child: panel=%+v controls=%v", m.panel, controls)
	}
	m.Update(SubagentSnapshotMsg{
		Snapshot: SubagentSnapshot{Tasks: tasks, ViewingTaskID: "", Revision: 7},
		InputHistory: &InputHistoryState{
			Metadata: InputHistoryMetadata{Generation: 11, SessionID: "main"}, ScopeID: "main",
		},
	})
	if m.panel.viewingTaskID != "child" {
		t.Fatal("stale backend snapshot changed visible scope")
	}
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "child only")
}

func TestInputHistoryGenerationRequiresMainReset(t *testing.T) {
	for _, state := range []InputHistoryState{
		{Metadata: InputHistoryMetadata{Generation: 10, SessionID: "stale"}, ScopeID: "stale", ResetAll: true},
		{Metadata: InputHistoryMetadata{Generation: 12, SessionID: "next"}, ScopeID: "next-child"},
	} {
		t.Run(state.ScopeID, func(t *testing.T) {
			m, _ := persistentHistoryModel(0, ComposerHistoryEntry{Draft: ComposerDraft{Text: "current input"}})
			m.Update(OperationDoneMsg{InputHistory: &state})
			press(m, tea.KeyUp)
			assertHistoryText(t, m, "current input")
		})
	}
}

func TestInputHistoryViewChangePreservesLocalOffsets(t *testing.T) {
	m, requests := persistentHistoryModel(2,
		ComposerHistoryEntry{Draft: ComposerDraft{Text: "local-old"}},
		ComposerHistoryEntry{Draft: ComposerDraft{Text: "local-new"}},
	)
	press(m, tea.KeyUp)
	press(m, tea.KeyUp)
	press(m, tea.KeyUp)
	request := latestHistoryRequest(t, requests, 1)
	m.Update(HistoryLookupMsg{Request: request, Err: errors.New("history file replaced"), Unavailable: true})
	assertHistoryText(t, m, "local-old")
	press(m, tea.KeyUp)
	assertHistoryText(t, m, "local-old")
	press(m, tea.KeyDown)
	assertHistoryText(t, m, "local-new")
	if len(*requests) != 1 {
		t.Fatalf("invalidated view was read again: %v", *requests)
	}
}
