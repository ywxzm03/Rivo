package adapter

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/inputhistory"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tui"
)

type historyRuntimeFixture struct {
	runtime     *Runtime          // 真实 app、session、provider 接线
	service     *app.InputHistory // 同一进程内由 fixture 持有
	historyPath string
	skillPath   string
	sessionDir  string
}

// newHistoryRuntime 复用现有本地 SSE 模型，历史文件独立于会话文件
func newHistoryRuntime(t *testing.T, handler http.HandlerFunc) historyRuntimeFixture {
	t.Helper()
	r, skillPath, sessionDir := newSkillRuntime(t, handler)
	path := filepath.Join(os.Getenv("RIVO_AGENT_DIR"), "history.jsonl")
	service := app.NewInputHistory(path, func(origin llm.UserInputOrigin, err error) {
		r.mu.Lock()
		send := r.send
		r.mu.Unlock()
		if send != nil {
			send(tui.HistoryRecordMsg{Origin: tui.InputOrigin{
				Generation: origin.Generation, Sequence: origin.Sequence, SessionID: origin.SessionID, Text: origin.Text,
			}, Err: err})
		}
	})
	r.BindInputHistory(service)
	r.Attach(r.app)
	t.Cleanup(func() {
		r.app.Close()
		if failures := service.Close(); len(failures) != 0 {
			t.Errorf("unreported history failures: %v", failures)
		}
	})
	return historyRuntimeFixture{runtime: r, service: service, historyPath: path, skillPath: skillPath, sessionDir: sessionDir}
}

func historyRuntimeState(t *testing.T, r *Runtime) *tui.InputHistoryState {
	t.Helper()
	state := r.Snapshot().InputHistory
	if state == nil {
		t.Fatal("attached runtime did not publish input history state")
	}
	return state
}

func historyRuntimeRequest(t *testing.T, r *Runtime, sequence uint64, text string) tui.Request {
	t.Helper()
	state := historyRuntimeState(t, r)
	return tui.Request{Kind: tui.RequestRun, Value: text, Payload: &tui.ComposerPayload{Text: text, Origin: &tui.InputOrigin{
		Generation: state.Metadata.Generation, Sequence: sequence, SessionID: state.ScopeID, Text: text,
	}}}
}

func historyOperationDone(t *testing.T, cmd tea.Cmd) tui.OperationDoneMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("operation returned no command")
	}
	done, ok := cmd().(tui.OperationDoneMsg)
	if !ok {
		t.Fatal("operation did not produce a completion message")
	}
	return done
}

func historyRuntimeRows(t *testing.T, path string) []inputhistory.Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		t.Fatalf("history ends with an uncommitted tail: %q", data)
	}
	var records []inputhistory.Record
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var record inputhistory.Record
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestInputHistorySkillUsesRawInvocation(t *testing.T) {
	payloads := make(chan string, 1)
	fixture := newHistoryRuntime(t, func(w http.ResponseWriter, request *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		payloads <- string(body)
		completeSkillResponse(w)
	})
	r := fixture.runtime
	raw := "/skill:pdf-tools arguments"
	request := historyRuntimeRequest(t, r, 1, raw)
	done := historyOperationDone(t, r.Request(request))
	if done.Err != nil {
		t.Fatal(done.Err)
	}
	fixture.service.Barrier()
	rows := historyRuntimeRows(t, fixture.historyPath)
	if len(rows) != 1 || rows[0].Text != raw || rows[0].SessionID != request.Payload.Origin.SessionID {
		t.Fatalf("skill history = %+v, want original invocation and receiver", rows)
	}
	modelBody := <-payloads
	if !strings.Contains(modelBody, "Original instructions") || !strings.Contains(modelBody, "arguments") || strings.Contains(modelBody, "Generation") || strings.Contains(modelBody, "Sequence") {
		t.Fatalf("model received wrong skill content or transient origin: %s", modelBody)
	}
	if err := os.Remove(fixture.skillPath); err != nil {
		t.Fatal(err)
	}
	failed := historyOperationDone(t, r.Request(historyRuntimeRequest(t, r, 2, raw)))
	if failed.Err == nil {
		t.Fatal("missing skill file was accepted")
	}
	fixture.service.Barrier()
	if rows := historyRuntimeRows(t, fixture.historyPath); len(rows) != 1 || rows[0].Text != raw {
		t.Fatalf("failed expansion added history: %+v", rows)
	}
}

func TestInputHistoryResumeAndMainScopeSeed(t *testing.T) {
	fixture := newHistoryRuntime(t, func(w http.ResponseWriter, _ *http.Request) { completeSkillResponse(w) })
	r := fixture.runtime
	original := *historyRuntimeState(t, r)
	raw := "/skill:pdf-tools arguments"
	if done := historyOperationDone(t, r.Request(historyRuntimeRequest(t, r, 1, raw))); done.Err != nil {
		t.Fatal(done.Err)
	}
	files, err := filepath.Glob(filepath.Join(fixture.sessionDir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("saved sessions = %v, err=%v", files, err)
	}
	if done := historyOperationDone(t, r.Request(tui.Request{Kind: tui.RequestNew})); done.Err != nil {
		t.Fatal(done.Err)
	}
	fresh := historyRuntimeState(t, r)
	if fresh.Metadata.Generation == original.Metadata.Generation || fresh.ScopeID == original.ScopeID || fresh.Metadata.PersistentCount != 1 || len(fresh.Replay) != 0 {
		t.Fatalf("new session history = %+v, original=%+v", fresh, original)
	}
	if done := historyOperationDone(t, r.Request(tui.Request{Kind: tui.RequestResume, Value: files[0]})); done.Err != nil {
		t.Fatal(done.Err)
	}
	resumed := historyRuntimeState(t, r)
	if resumed.ScopeID != original.ScopeID || resumed.Metadata.Generation == fresh.Metadata.Generation || !resumed.ResetAll || resumed.Metadata.PersistentCount != 1 {
		t.Fatalf("resume installed wrong generation or receiver: %+v", resumed)
	}
	if len(resumed.Replay) != 1 || resumed.Replay[0].Draft.Text != raw || !resumed.Replay[0].Replayed {
		t.Fatalf("resume did not seed original skill invocation: %+v", resumed.Replay)
	}
}

func TestInputHistoryClearKeepsLocalAndSession(t *testing.T) {
	fixture := newHistoryRuntime(t, func(w http.ResponseWriter, _ *http.Request) { completeSkillResponse(w) })
	r := fixture.runtime
	m := tui.New(r.Snapshot(), tui.Handlers{Request: r.Request, History: r.HistoryHandlers()})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	previous := submitAndSettle(t, m, "previous")
	if previous.Err != nil {
		t.Fatal(previous.Err)
	}
	m.Update(previous)
	before := *historyRuntimeState(t, r)
	// 在本 UI 中记录取消草稿，再执行清上下文
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("canceled draft")})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	done := submitAndSettle(t, m, "/clear")
	if done.Err != nil {
		t.Fatal(done.Err)
	}
	m.Update(done)
	after := historyRuntimeState(t, r)
	if after.Metadata != before.Metadata || after.ScopeID != before.ScopeID || r.app.SessionID() != before.ScopeID {
		t.Fatalf("clear changed input history lifecycle: before=%+v after=%+v", before, after)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if view := m.View(); !strings.Contains(view, "canceled draft") {
		t.Fatalf("clear erased local canceled draft: %q", view)
	}
	fixture.service.Barrier()
	rows := historyRuntimeRows(t, fixture.historyPath)
	if len(rows) != 2 || rows[0].Text != "previous" || rows[1].Text != "canceled draft" {
		t.Fatalf("clear recorded itself or lost input: %+v", rows)
	}
}

func TestInputHistoryGlobalRecallAcrossRuntime(t *testing.T) {
	fixture := newHistoryRuntime(t, func(w http.ResponseWriter, _ *http.Request) { completeSkillResponse(w) })
	r := fixture.runtime
	if done := historyOperationDone(t, r.Request(historyRuntimeRequest(t, r, 1, "from another project"))); done.Err != nil {
		t.Fatal(done.Err)
	}
	r.app.Close()
	if failures := fixture.service.Close(); len(failures) != 0 {
		t.Fatal(failures)
	}
	second := New(r.cfg, filepath.Join(t.TempDir(), "different-project"), "")
	service := app.NewInputHistory(fixture.historyPath, nil)
	t.Cleanup(func() {
		if failures := service.Close(); len(failures) != 0 {
			t.Error(failures)
		}
	})
	second.BindInputHistory(service)
	a, err := app.New(r.cfg, second.HandleEvent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	second.Attach(a)
	state := historyRuntimeState(t, second)
	if state.Metadata.PersistentCount != 1 || state.ScopeID == r.app.SessionID() {
		t.Fatalf("second runtime history = %+v", state)
	}
	request := tui.HistoryLookupRequest{Generation: state.Metadata.Generation, ScopeID: state.ScopeID, RequestID: 1, Offset: 0}
	message, ok := second.HistoryHandlers().Lookup(request)().(tui.HistoryLookupMsg)
	if !ok || message.Err != nil || message.Text != "from another project" {
		t.Fatalf("global recall = %+v, typed=%v", message, ok)
	}
}

func TestInputHistoryCompactionStageAdmission(t *testing.T) {
	for _, success := range []bool{true, false} {
		name := "failure restores draft"
		if success {
			name = "success admits staged inputs"
		}
		t.Run(name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var first atomic.Bool
			fixture := newHistoryRuntime(t, func(w http.ResponseWriter, request *http.Request) {
				if first.CompareAndSwap(false, true) {
					close(started)
					select {
					case <-release:
					case <-request.Context().Done():
						return
					}
				}
				completeSkillResponse(w)
			})
			r := fixture.runtime
			var run tea.Cmd
			m := tui.NewWithInitialPrompt(r.Snapshot(), tui.Handlers{
				Request: func(request tui.Request) tea.Cmd { run = r.Request(request); return run },
				History: r.HistoryHandlers(), EnqueueSteeringMessage: r.EnqueueSteeringMessage, EnqueueFollowUpMessage: r.EnqueueFollowUpMessage,
			}, "initial run")
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
			m.Init()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("initial model request did not start")
			}
			m.Update(tui.StatusMsg("Compacting context"))
			for i, text := range []string{"staged steering", "staged follow-up"} {
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
				key := tea.KeyEnter
				if i == 1 {
					key = tea.KeyTab
				}
				m.Update(tea.KeyMsg{Type: key})
			}
			fixture.service.Barrier()
			if rows := historyRuntimeRows(t, fixture.historyPath); len(rows) != 1 || rows[0].Text != "initial run" {
				t.Fatalf("staged input persisted before admission: %+v", rows)
			}
			status := "Compaction failed"
			if success {
				status = "Working"
			}
			m.Update(tui.StatusMsg(status))
			// 暂存输入在同一个真实运行中消费，不额外启动替代运行
			close(release)
			done := historyOperationDone(t, run)
			if done.Err != nil {
				t.Fatal(done.Err)
			}
			fixture.service.Barrier()
			rows := historyRuntimeRows(t, fixture.historyPath)
			if success {
				if len(rows) != 3 || rows[0].Text != "initial run" || rows[1].Text != "staged steering" || rows[2].Text != "staged follow-up" {
					t.Fatalf("consumed staged history = %+v", rows)
				}
			} else if len(rows) != 1 || rows[0].Text != "initial run" || !strings.Contains(m.View(), "staged steering") || !strings.Contains(m.View(), "staged follow-up") {
				t.Fatalf("failed compaction lost draft or recorded unsent input: rows=%+v view=%q", rows, m.View())
			}
		})
	}
}

func TestInputHistoryAdmissionErrorDoesNotBlockUpdate(t *testing.T) {
	fixture := newHistoryRuntime(t, func(w http.ResponseWriter, _ *http.Request) { completeSkillResponse(w) })
	r := fixture.runtime
	if failures := fixture.service.Close(); len(failures) != 0 {
		t.Fatal(failures)
	}
	releaseSend := make(chan struct{})
	defer close(releaseSend)
	attemptedSend := make(chan tea.Msg, 1)
	r.send = func(message tea.Msg) {
		attemptedSend <- message
		<-releaseSend
	}
	m := tui.New(r.Snapshot(), tui.Handlers{History: r.HistoryHandlers()})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("cleared after service close")})
	returned := make(chan tea.Cmd, 1)
	go func() {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		returned <- cmd
	}()
	var cmd tea.Cmd
	select {
	case cmd = <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("synchronous admission failure blocked Model.Update on program.Send")
	}
	if cmd == nil {
		t.Fatal("rejected cleared draft returned no error command")
	}
	reply, ok := cmd().(tui.HistoryRecordMsg)
	if !ok || reply.Err == nil {
		t.Fatalf("rejected draft reply = %+v, typed=%v", reply, ok)
	}
	m.Update(reply)
	if !strings.Contains(m.View(), reply.Err.Error()) {
		t.Fatalf("admission failure was not visible: %q", m.View())
	}
	if failures := fixture.service.Close(); len(failures) != 0 {
		t.Fatalf("UI-acknowledged rejection remained unreported: %v", failures)
	}
	if rows := historyRuntimeRows(t, fixture.historyPath); len(rows) != 0 {
		t.Fatalf("rejected input was persisted: %+v", rows)
	}
	select {
	case message := <-attemptedSend:
		t.Fatalf("synchronous rejection used program.Send instead of returned Cmd: %T", message)
	default:
	}
}
