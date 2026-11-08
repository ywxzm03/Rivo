package adapter

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tool/codemode"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func codemodeRuntime() *Runtime {
	return &Runtime{sequence: 1, currentAssistantID: "assistant-1"}
}

func startCodemode(r *Runtime) {
	r.toolStartLocked(llm.ToolExecutionStartEvent{ToolCallID: "parent", ToolName: "codemode", Arguments: map[string]any{"code": "text('body');"}})
}

func codemodeParent(t *testing.T, messages []tui.Message) tui.ToolCallView {
	t.Helper()
	if len(messages) != 1 || messages[0].ToolGroup == nil || len(messages[0].ToolGroup.Calls) != 1 {
		t.Fatalf("want only the parent tool row, got %+v", messages)
	}
	call := messages[0].ToolGroup.Calls[0]
	if call.ID != "parent" || call.Name != "codemode" {
		t.Fatalf("unexpected parent row: %+v", call)
	}
	return call
}

func codemodeFinalResult(failed bool) llm.ToolResult {
	details := codemode.Details{
		Codemode: true, WallTimeMS: 0, OutputLines: 120, FullOutputPath: "/tmp/full-output.txt",
		Calls: []codemode.Call{
			{ID: "parent/1", Name: "edit", Args: `{"path":"a.go"}`, Status: codemode.CallOK, DurationMS: new(int64(0))},
			{ID: "parent/2", Name: "read", Args: `{"path":"b.go"}`, Status: codemode.CallError, Error: "caught child failure", DurationMS: new(int64(12))},
			{ID: "parent/3", Name: "read", Args: `{"path":"c.go"}`, Status: codemode.CallOK, DurationMS: new(int64(3))},
			{ID: "parent/4", Name: "bash", Args: `{"command":"sleep 1"}`, Status: codemode.CallCancelled},
		},
	}
	result := llm.ToolResult{Content: []llm.Content{
		llm.TextContent{Text: "Opaque status header, not an English prefix"},
		llm.TextContent{Text: "body\n[image: image/png]"},
		llm.ImageContent{MIMEType: "image/png", Data: "BASE64_MUST_NOT_REACH_TUI"},
	}, IsError: failed}
	if failed {
		details.Error = "Error: parent boom\n at codemode.js:4:2"
		result.Content = append(result.Content, llm.TextContent{Text: details.Error})
	} else {
		details.StoreWrites = &codemode.StoreWrites{SessionID: "private-session", Set: map[string]json.RawMessage{"secret": json.RawMessage(`"STORE_MUST_NOT_REACH_TUI"`)}, Delete: []string{"removed"}}
	}
	result.Details = details
	return result
}

func TestCodemodeLiveAndReplayProjection(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success-with-caught-child-error"
		if failed {
			name = "parent-failure"
		}
		t.Run(name, func(t *testing.T) {
			r := codemodeRuntime()
			startCodemode(r)
			r.toolUpdateLocked(llm.ToolExecutionUpdateEvent{ToolCallID: "parent", ToolName: "codemode", PartialResult: llm.ToolResult{Details: codemode.Details{Codemode: true, Calls: []codemode.Call{{ID: "parent/1", Name: "edit", Status: codemode.CallRunning}}}}})
			live := codemodeParent(t, r.messages)
			if live.Codemode == nil || live.Codemode.Summary.Complete || live.Codemode.Output.DurationMS != nil || live.Codemode.Calls[0].Status != tui.CodemodeRunning {
				t.Fatalf("live snapshot treated as final: %+v", live.Codemode)
			}
			published := r.messages[0].ToolGroup.Clone()
			result := codemodeFinalResult(failed)
			r.toolEndLocked(llm.ToolExecutionEndEvent{ToolCallID: "parent", ToolName: "codemode", Result: result, IsError: failed})
			final := codemodeParent(t, r.messages)
			if final.Codemode == nil {
				t.Fatal("recognized Details did not create Codemode view")
			}
			if !final.Codemode.Summary.Complete || final.Codemode.Output.LineCount != 120 || final.Codemode.Output.DurationMS == nil || *final.Codemode.Output.DurationMS != 0 || final.Codemode.Output.FullOutputPath != "/tmp/full-output.txt" {
				t.Fatalf("final metadata must use authoritative Details: %+v", final.Codemode)
			}
			if final.Result != "body\n[image: image/png]" || final.Details != "" {
				t.Fatalf("content boundaries not stripped: %+v", final)
			}
			wantGroups := []tui.CodemodeToolCountView{{Name: "read", Success: 1, Failure: 1}, {Name: "bash", Unfinished: 1}, {Name: "edit", Success: 1}}
			if !reflect.DeepEqual(final.Codemode.Summary.Groups, wantGroups) {
				t.Fatalf("groups = %+v, want %+v", final.Codemode.Summary.Groups, wantGroups)
			}
			if final.Codemode.Calls[0].Name != "edit" || final.Codemode.Calls[1].Name != "read" || final.Codemode.Calls[0].DurationMS == nil || *final.Codemode.Calls[0].DurationMS != 0 {
				t.Fatalf("issuance order or zero duration lost: %+v", final.Codemode.Calls)
			}
			wantStatus := tui.ToolSuccess
			wantError := ""
			if failed {
				wantStatus = tui.ToolError
				wantError = "Error: parent boom\n at codemode.js:4:2"
			}
			if final.Status != wantStatus || final.Codemode.Error != wantError {
				t.Fatalf("parent status/error = %+v", final)
			}
			if published.Calls[0].Codemode.Calls[0].Status != tui.CodemodeRunning || published.Calls[0].Codemode.Summary.Complete {
				t.Fatal("final update mutated previously published live snapshot")
			}
			replayed := r.projectContext(testContext([]llm.Message{
				llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "parent", Name: "codemode", Arguments: map[string]any{"code": "text('body');"}}}},
				llm.ToolResultMessage{ToolCallID: "parent", ToolName: "codemode", Content: result.Content, Details: result.Details, IsError: failed},
			}))
			replay := codemodeParent(t, replayed)
			if !reflect.DeepEqual(final.Codemode, replay.Codemode) || final.Result != replay.Result || final.Status != replay.Status || replay.Details != "" {
				t.Fatalf("live/end/replay diverged:\nfinal=%+v\nreplay=%+v", final, replay)
			}
			data, err := json.Marshal(replayed)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"BASE64_MUST_NOT_REACH_TUI", "STORE_MUST_NOT_REACH_TUI", "private-session", "storeWrites", "Opaque status header", "image image/png"} {
				if strings.Contains(string(data), forbidden) {
					t.Fatalf("projection leaked %q: %s", forbidden, data)
				}
			}
		})
	}
}

func TestCodemodeEmptyProgressPreservesObservedCalls(t *testing.T) {
	r := codemodeRuntime()
	startCodemode(r)
	r.toolUpdateLocked(llm.ToolExecutionUpdateEvent{ToolCallID: "parent", ToolName: "codemode", PartialResult: llm.ToolResult{Details: codemode.Details{Codemode: true, Calls: []codemode.Call{{ID: "parent/1", Name: "read", Status: codemode.CallRunning}}}}})
	before := r.messages[0].ToolGroup.Clone()
	r.toolUpdateLocked(llm.ToolExecutionUpdateEvent{ToolCallID: "parent", ToolName: "codemode"})
	if got := codemodeParent(t, r.messages); !reflect.DeepEqual(got.Codemode, before.Calls[0].Codemode) {
		t.Fatalf("empty progress discarded observed state: %+v", got.Codemode)
	}
}

func TestCodemodeFinalWithoutDetailsClearsLiveState(t *testing.T) {
	r := codemodeRuntime()
	startCodemode(r)
	r.toolUpdateLocked(llm.ToolExecutionUpdateEvent{ToolCallID: "parent", ToolName: "codemode", PartialResult: llm.ToolResult{Details: codemode.Details{Codemode: true, Calls: []codemode.Call{{ID: "parent/1", Name: "read", Status: codemode.CallOK}}}}})
	content := []llm.Content{llm.TextContent{Text: "raw first block"}, llm.TextContent{Text: "raw second block"}}
	r.toolEndLocked(llm.ToolExecutionEndEvent{ToolCallID: "parent", ToolName: "codemode", Result: llm.ToolResult{Content: content}})
	got := codemodeParent(t, r.messages)
	if got.Codemode != nil || got.Result != "raw first block\nraw second block" {
		t.Fatalf("missing final Details retained fabricated statistics or stripped raw content: %+v", got)
	}
}

func TestCodemodeLateProgressCannotRegressFinalOrRevision(t *testing.T) {
	r := codemodeRuntime()
	startCodemode(r)
	r.toolEndLocked(llm.ToolExecutionEndEvent{ToolCallID: "parent", ToolName: "codemode", Result: codemodeFinalResult(false)})
	before, revision := r.messages[0].ToolGroup.Clone(), r.messages[0].Revision
	r.toolUpdateLocked(llm.ToolExecutionUpdateEvent{ToolCallID: "parent", ToolName: "codemode", PartialResult: llm.ToolResult{Details: codemode.Details{Codemode: true, Calls: []codemode.Call{{ID: "parent/1", Name: "edit", Status: codemode.CallRunning}}}}})
	if !reflect.DeepEqual(r.messages[0].ToolGroup, before) || r.messages[0].Revision != revision {
		t.Fatalf("late update changed final data or Revision: revision %d -> %d, call=%+v", revision, r.messages[0].Revision, codemodeParent(t, r.messages))
	}
}

func TestCodemodeMissingFinalSealCancelsNestedCallsAndChangesRevision(t *testing.T) {
	r := codemodeRuntime()
	startCodemode(r)
	r.toolUpdateLocked(llm.ToolExecutionUpdateEvent{ToolCallID: "parent", ToolName: "codemode", PartialResult: llm.ToolResult{Details: codemode.Details{Codemode: true, Calls: []codemode.Call{
		{ID: "parent/1", Name: "edit", Status: codemode.CallQueued},
		{ID: "parent/2", Name: "read", Status: codemode.CallRunning, DurationMS: new(int64(0))},
		{ID: "parent/3", Name: "read", Status: codemode.CallOK, DurationMS: new(int64(2))},
	}}}})
	published, revision := r.messages[0].ToolGroup.Clone(), r.messages[0].Revision
	r.applyAgentEndLocked(llm.AgentEndEvent{}, &eventResult{})
	got := codemodeParent(t, r.messages)
	if got.Status != tui.ToolInterrupted || got.Codemode == nil || got.Codemode.Summary.Complete || got.Codemode.Output.DurationMS != nil {
		t.Fatalf("missing final fabricated completion: %+v", got)
	}
	if got.Codemode.Calls[0].Status != tui.CodemodeCancelled || got.Codemode.Calls[1].Status != tui.CodemodeCancelled || got.Codemode.Calls[2].Status != tui.CodemodeOK {
		t.Fatalf("seal left nested calls running or changed completed call: %+v", got.Codemode.Calls)
	}
	wantGroups := []tui.CodemodeToolCountView{{Name: "read", Success: 1, Unfinished: 1}, {Name: "edit", Unfinished: 1}}
	if !reflect.DeepEqual(got.Codemode.Summary.Groups, wantGroups) || r.messages[0].Revision <= revision {
		t.Fatalf("seal did not update groups/revision: %+v, %d -> %d", got.Codemode.Summary, revision, r.messages[0].Revision)
	}
	if published.Calls[0].Codemode.Calls[0].Status != tui.CodemodeQueued || published.Calls[0].Codemode.Calls[1].Status != tui.CodemodeRunning {
		t.Fatal("seal mutated prior published snapshot")
	}
}

func TestCodemodeHistoryWithoutRecognizedDetailsPreservesRawBody(t *testing.T) {
	for _, details := range []llm.ToolDetails{nil, codemode.Details{Codemode: false}} {
		r := codemodeRuntime()
		projected := r.projectContext(testContext([]llm.Message{
			llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "parent", Name: "codemode"}}},
			llm.ToolResultMessage{ToolCallID: "parent", ToolName: "codemode", Details: details, Content: []llm.Content{llm.TextContent{Text: "raw header"}, llm.TextContent{Text: "raw body"}}},
		}))
		got := codemodeParent(t, projected)
		if got.Codemode != nil || got.Result != "raw header\nraw body" {
			t.Fatalf("unrecognized marker stripped raw history: %+v", got)
		}
	}
}

func TestCodemodeKnownEmptyFinalAndInvalidBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   []llm.Content
		wantKnown bool
		wantBody  string
	}{
		{"empty output", []llm.Content{llm.TextContent{Text: "status"}}, true, ""},
		{"no header", nil, false, ""},
		{"image header", []llm.Content{llm.ImageContent{MIMEType: "image/png", Data: "image-data"}, llm.TextContent{Text: "raw body"}}, false, "[image image/png, 10 bytes]\nraw body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := codemodeRuntime()
			startCodemode(r)
			r.toolEndLocked(llm.ToolExecutionEndEvent{ToolCallID: "parent", ToolName: "codemode", Result: llm.ToolResult{Content: tc.content, Details: codemode.Details{Codemode: true, Calls: []codemode.Call{}}}})
			got := codemodeParent(t, r.messages)
			if (got.Codemode != nil) != tc.wantKnown || got.Result != tc.wantBody {
				t.Fatalf("boundary handling = %+v, want known=%v body=%q", got, tc.wantKnown, tc.wantBody)
			}
			if tc.wantKnown && (!got.Codemode.Summary.Complete || len(got.Codemode.Summary.Groups) != 0 || got.Codemode.Output.LineCount != 0 || got.Codemode.Output.DurationMS == nil || *got.Codemode.Output.DurationMS != 0) {
				t.Fatalf("known zero metadata lost: %+v", got.Codemode)
			}
		})
	}
}

func TestCodemodeMissingErrorTailPreservesRawContent(t *testing.T) {
	r := codemodeRuntime()
	startCodemode(r)
	r.toolEndLocked(llm.ToolExecutionEndEvent{ToolCallID: "parent", ToolName: "codemode", Result: llm.ToolResult{
		Content: []llm.Content{llm.TextContent{Text: "raw status without independent error tail"}},
		Details: codemode.Details{Codemode: true, Error: "Error: missing tail", OutputLines: 99},
		IsError: true,
	}})
	got := codemodeParent(t, r.messages)
	if got.Codemode != nil || got.Result != "raw status without independent error tail" || got.Status != tui.ToolError {
		t.Fatalf("invalid failure boundary stripped raw result or invented statistics: %+v", got)
	}
}

func TestCodemodeUnchangedProgressDoesNotPublishOrBumpRevision(t *testing.T) {
	r := codemodeRuntime()
	startCodemode(r)
	event := llm.ToolExecutionUpdateEvent{ToolCallID: "parent", ToolName: "codemode", PartialResult: llm.ToolResult{Details: codemode.Details{
		Codemode: true, Calls: []codemode.Call{{ID: "parent/1", Name: "read", Status: codemode.CallRunning, DurationMS: new(int64(0))}},
	}}}
	if err := r.handleEvent(event); err != nil {
		t.Fatal(err)
	}
	revision := r.messages[0].Revision
	for _, update := range []llm.ToolExecutionUpdateEvent{event, {ToolCallID: "parent", ToolName: "codemode"}} {
		result := eventResult{}
		r.applyEventLocked(update, &result)
		if result.message != nil || r.messages[0].Revision != revision {
			t.Fatalf("unchanged progress published or changed revision: message=%+v, %d -> %d", result.message, revision, r.messages[0].Revision)
		}
		if err := r.handleEvent(update); err != nil {
			t.Fatal(err)
		}
		if r.messages[0].Revision != revision {
			t.Fatalf("event handling bumped unchanged revision: %d -> %d", revision, r.messages[0].Revision)
		}
	}
	r.applyAgentEndLocked(llm.AgentEndEvent{}, &eventResult{})
	revision = r.messages[0].Revision
	if err := r.handleEvent(event); err != nil {
		t.Fatal(err)
	}
	if len(r.messages) != 1 || r.messages[0].Revision != revision || r.messages[0].ToolGroup.Calls[0].Status != tui.ToolInterrupted {
		t.Fatalf("late progress reopened sealed parent: %+v", r.messages)
	}
}

func TestCodemodeProjectionOwnsDomainPointersAndPublishedSnapshots(t *testing.T) {
	r := codemodeRuntime()
	startCodemode(r)
	duration := new(int64(7))
	details := codemode.Details{Codemode: true, Calls: []codemode.Call{{ID: "parent/1", Name: "read", Status: codemode.CallRunning, DurationMS: duration}}}
	r.toolUpdateLocked(llm.ToolExecutionUpdateEvent{ToolCallID: "parent", ToolName: "codemode", PartialResult: llm.ToolResult{Details: &details}})
	published := r.publishLocked(0)
	*duration = 99
	details.Calls[0].Name = "mutated-domain"
	if got := codemodeParent(t, r.messages); got.Codemode.Calls[0].Name != "read" || *got.Codemode.Calls[0].DurationMS != 7 {
		t.Fatalf("projection retained domain pointers: %+v", got.Codemode.Calls)
	}
	r.sealToolGroupLocked()
	if got := published.ToolGroup.Calls[0].Codemode; got.Calls[0].Status != tui.CodemodeRunning || *got.Calls[0].DurationMS != 7 || got.Summary.Groups[0].Unfinished != 1 {
		t.Fatalf("seal mutated published snapshot: %+v", got)
	}
}
