package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tool/codemode"
)

func codemodeRecord(sessionID, value string) llm.ToolResultMessage {
	return llm.ToolResultMessage{
		ToolName: "codemode", ToolCallID: value,
		Content: []llm.Content{llm.TextContent{Text: "state"}},
		Details: codemode.Details{Codemode: true, Calls: []codemode.Call{}, StoreWrites: &codemode.StoreWrites{
			SessionID: sessionID, Set: map[string]json.RawMessage{"value": json.RawMessage(`"` + value + `"`)},
		}},
	}
}

func assertBranchCodemodeValues(t *testing.T, m *SessionManager, want ...string) {
	t.Helper()
	raw, err := m.BranchToolDetails("codemode")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(raw))
	for _, data := range raw {
		var details codemode.Details
		if err := json.Unmarshal(data, &details); err != nil {
			t.Fatal(err)
		}
		if details.StoreWrites == nil {
			t.Fatalf("missing writes in %s", data)
		}
		var value string
		if err := json.Unmarshal(details.StoreWrites.Set["value"], &value); err != nil {
			t.Fatal(err)
		}
		got = append(got, value)
	}
	if !reflect.DeepEqual(got, append([]string{}, want...)) {
		t.Fatalf("branch values = %v, want %v", got, want)
	}
}

func TestBranchToolDetailsUsesOriginalActiveParentChain(t *testing.T) {
	m := New(t.TempDir(), t.TempDir())
	appendMessage := func(message llm.Message) string {
		t.Helper()
		if err := m.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
		return m.LeafID()
	}
	ancestor := appendMessage(codemodeRecord(m.ID(), "ancestor"))
	user := appendMessage(userMsg("change state"))
	appendMessage(codemodeRecord(m.ID(), "main"))
	failed := codemodeRecord(m.ID(), "failed")
	failed.IsError = true
	appendMessage(failed)
	other := codemodeRecord(m.ID(), "other")
	other.ToolName = "read"
	appendMessage(other)
	appendMessage(llm.ToolResultMessage{ToolName: "codemode", ToolCallID: "legacy"})
	assertBranchCodemodeValues(t, m, "ancestor", "main")
	if err := m.AppendContextEdit(ancestor, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendCompaction("summary", "", 100, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.BuildContext().Messages()) != 1 {
		t.Fatal("compaction did not hide the original tool results")
	}
	assertBranchCodemodeValues(t, m, "ancestor", "main")
	if _, err := m.RewindUser(user); err != nil {
		t.Fatal(err)
	}
	assertBranchCodemodeValues(t, m, "ancestor")
	appendMessage(codemodeRecord(m.ID(), "side"))
	assertBranchCodemodeValues(t, m, "ancestor", "side")
	reopened, err := Open(m.Path(), m.Cwd())
	if err != nil {
		t.Fatal(err)
	}
	assertBranchCodemodeValues(t, reopened, "ancestor", "side")
	if err := m.Branch(ancestor); err != nil {
		t.Fatal(err)
	}
	assertBranchCodemodeValues(t, m, "ancestor")
	if err := m.ResetLeaf(); err != nil {
		t.Fatal(err)
	}
	assertBranchCodemodeValues(t, m)
	appendMessage(codemodeRecord(m.ID(), "fresh"))
	assertBranchCodemodeValues(t, m, "fresh")
}

func TestCodemodeDetailsRoundTrip(t *testing.T) {
	for _, details := range []codemode.Details{
		{Codemode: true, Calls: []codemode.Call{}, OutputLines: 0},
		{Codemode: true, Calls: []codemode.Call{
			{ID: "parent/1", Name: "read", Args: `{"path":"文件"}`, Status: codemode.CallOK, DurationMS: new(int64(0))},
			{ID: "parent/2", Name: "write", Args: `{}`, Status: codemode.CallError, DurationMS: new(int64(5)), Error: "denied"},
			{ID: "parent/3", Name: "edit", Args: `{}`, Status: codemode.CallCancelled},
		}, WallTimeMS: 6, OutputLines: 2, Error: "script failed\nat codemode.js:2", FullOutputPath: "/tmp/full-output"},
		{Codemode: true, Calls: []codemode.Call{}, StoreWrites: &codemode.StoreWrites{
			SessionID: "session-id", Set: map[string]json.RawMessage{"null": json.RawMessage(`null`), "object": json.RawMessage(`{"items":[1,true,"中文"]}`)}, Delete: []string{"removed"},
		}},
	} {
		m := New(t.TempDir(), t.TempDir())
		if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "run script"}}}); err != nil {
			t.Fatal(err)
		}
		message := llm.ToolResultMessage{ToolName: "codemode", ToolCallID: "parent", Details: details, IsError: details.Error != ""}
		if err := m.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(m.Path(), m.Cwd())
		if err != nil {
			t.Fatal(err)
		}
		messages := reopened.BuildContext().Messages()
		if len(messages) != 2 {
			t.Fatalf("reopened messages = %v", messages)
		}
		got, ok := messages[1].(llm.ToolResultMessage)
		if !ok {
			t.Fatalf("message type = %T", messages[1])
		}
		if !reflect.DeepEqual(got.Details, details) || got.IsError != message.IsError {
			t.Fatalf("roundtrip = %#v, want %#v", got, message)
		}
	}
}

type codemodeUnencodableDetails struct{}

func (codemodeUnencodableDetails) ToolDetails() {}
func (codemodeUnencodableDetails) MarshalJSON() ([]byte, error) {
	return nil, errors.New("details encoding failed")
}

func TestBranchToolDetailsReturnsEncodingFailure(t *testing.T) {
	m := New(t.TempDir(), t.TempDir())
	if err := m.AppendMessage(codemodeRecord(m.ID(), "initial")); err != nil {
		t.Fatal(err)
	}
	entry, ok := m.byID[m.LeafID()].(*MessageEntry)
	if !ok {
		t.Fatal("missing result entry")
	}
	entry.Message = llm.ToolResultMessage{ToolName: "codemode", Details: codemodeUnencodableDetails{}}
	if _, err := m.BranchToolDetails("codemode"); err == nil {
		t.Fatal("unencodable authoritative details were silently discarded")
	}
}

func TestCodemodeMalformedDetailsDecodeFails(t *testing.T) {
	for _, payload := range []string{
		`{"codemode":true,"calls":"broken"}`,
		`{"codemode":true,"storeWrites":{"sessionId":"s","set":[],"delete":[]}}`,
	} {
		_, err := decodeMessage(json.RawMessage(`{"role":"toolResult","toolCallId":"parent","toolName":"codemode","content":[],"details":` + payload + `}`))
		if err == nil {
			t.Fatalf("malformed details decoded as valid state: %s", payload)
		}
	}
}
