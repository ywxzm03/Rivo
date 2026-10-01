package responses

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestOutputSlotsPreserveInterleavedContent(t *testing.T) {
	partial := &llm.AssistantMessage{}
	slots := map[outputSlotKey]outputSlot{}
	var events []llm.Event
	send := func(event llm.Event) bool {
		events = append(events, event)
		return true
	}
	items := []responses.ResponseOutputItemUnion{
		responseItem(t, `{"type":"reasoning","id":"r1","summary":[],"content":[],"encrypted_content":"enc","status":"in_progress"}`),
		responseItem(t, `{"type":"message","id":"m1","role":"assistant","status":"in_progress","content":[]}`),
		responseItem(t, `{"type":"function_call","id":"f1","call_id":"c1","name":"read","arguments":""}`),
	}
	for i, item := range items {
		if !addOutputSlot(partial, slots, int64(i), item, send) {
			t.Fatal("addOutputSlot returned false")
		}
	}
	appendThinking(partial, 0, "plan")
	appendText(partial, 1, "answer")
	slots[outputSlotKey{outputIndex: 2}] = outputSlot{kind: llm.EventToolCallStart, contentIdx: 2, arguments: `{"path":"a.txt"}`}
	updateToolArguments(partial, 2, `{"path":"a.txt"}`)
	if got := partial.Content[0].(llm.ThinkingContent).Thinking; got != "plan" {
		t.Fatalf("thinking = %q", got)
	}
	if got := partial.Content[1].(llm.TextContent).Text; got != "answer" {
		t.Fatalf("text = %q", got)
	}
	if got := partial.Content[2].(llm.ToolCall).Arguments["path"]; got != "a.txt" {
		t.Fatalf("tool arguments = %#v", partial.Content[2])
	}
	if len(events) != 3 || events[0].ContentIndex != 0 || events[1].ContentIndex != 1 || events[2].ContentIndex != 2 {
		t.Fatalf("start events = %+v", events)
	}
}

func TestOutputSlotsSeparateMessageContentParts(t *testing.T) {
	partial := &llm.AssistantMessage{}
	slots := map[outputSlotKey]outputSlot{}
	var events []llm.Event
	send := func(event llm.Event) bool {
		events = append(events, event)
		return true
	}

	message := responseItem(t, `{"type":"message","id":"m1","role":"assistant","status":"in_progress","content":[]}`)
	if !addOutputSlot(partial, slots, 0, message, send) {
		t.Fatal("addOutputSlot returned false")
	}
	if !addContentPartSlot(partial, slots, 0, 1, "output_text", send) {
		t.Fatal("addContentPartSlot returned false")
	}
	appendText(partial, slots[outputSlotKey{outputIndex: 0}].contentIdx, "first")
	appendText(partial, slots[outputSlotKey{outputIndex: 0, contentIndex: 1}].contentIdx, "second")

	done := responseItem(t, `{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first","annotations":[]},{"type":"refusal","refusal":"second"}]}`)
	if !finishOutputSlot(partial, slots, 0, done, send) {
		t.Fatal("finishOutputSlot returned false")
	}
	if len(partial.Content) != 2 {
		t.Fatalf("partial content length = %d, want 2", len(partial.Content))
	}
	if got := partial.Content[0].(llm.TextContent).Text; got != "first" {
		t.Fatalf("first content = %q, want first", got)
	}
	if got := partial.Content[1].(llm.TextContent).Text; got != "second" {
		t.Fatalf("second content = %q, want second", got)
	}
	if len(events) != 4 || events[2].Type != llm.EventTextEnd || events[3].Type != llm.EventTextEnd || events[2].ContentIndex != 0 || events[3].ContentIndex != 1 {
		t.Fatalf("events = %+v, want independent text end events", events)
	}
}

func TestAddOutputSlotRejectsCustomTool(t *testing.T) {
	var event llm.Event
	accepted := addOutputSlot(&llm.AssistantMessage{}, map[outputSlotKey]outputSlot{}, 0,
		responseItem(t, `{"type":"custom_tool_call","id":"ct1","call_id":"call_custom","name":"apply_patch","input":"patch"}`),
		func(got llm.Event) bool {
			event = got
			return true
		})
	if accepted {
		t.Fatal("addOutputSlot accepted custom tool")
	}
	if event.Type != llm.EventError || event.Err == nil {
		t.Fatalf("event = %+v, want unsupported custom tool error", event)
	}
}

func TestToolCallStartIncludesDetails(t *testing.T) {
	partial := &llm.AssistantMessage{}
	var event llm.Event
	accepted := addOutputSlot(partial, map[outputSlotKey]outputSlot{}, 0,
		responseItem(t, `{"type":"function_call","id":"f1","call_id":"call-1","name":"read","arguments":"{\"path\":\"a.txt\"}"}`),
		func(got llm.Event) bool {
			event = got
			return true
		})
	if !accepted {
		t.Fatal("addOutputSlot returned false")
	}
	if event.Type != llm.EventToolCallStart {
		t.Fatalf("event type = %q, want toolcall_start", event.Type)
	}
	if event.ToolCall.ID != "call-1" || event.ToolCall.Name != "read" || event.ToolCall.Arguments["path"] != "a.txt" {
		t.Fatalf("tool call = %#v, want call-1/read/path=a.txt", event.ToolCall)
	}
}

func TestAssistantMessageSnapshotCopiesContent(t *testing.T) {
	message := &llm.AssistantMessage{
		ResponseID: "before",
		Content:    []llm.Content{llm.TextContent{Text: "before"}},
	}
	snapshot := assistantMessageSnapshot(message)

	message.ResponseID = "after"
	message.Content[0] = llm.TextContent{Text: "after"}

	if snapshot.ResponseID != "before" {
		t.Fatalf("snapshot ResponseID = %q, want before", snapshot.ResponseID)
	}
	if got := snapshot.Content[0].(llm.TextContent).Text; got != "before" {
		t.Fatalf("snapshot content = %q, want before", got)
	}
}

func TestIncompleteResponseError(t *testing.T) {
	tests := []struct {
		name    string
		status  responses.ResponseStatus
		reason  string
		wantErr bool
	}{
		{name: "completed", status: responses.ResponseStatusCompleted},
		{name: "max output tokens", status: responses.ResponseStatusIncomplete, reason: "max_output_tokens"},
		{name: "content filter", status: responses.ResponseStatusIncomplete, reason: "content_filter", wantErr: true},
		{name: "unknown incomplete", status: responses.ResponseStatusIncomplete, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := responses.Response{Status: tt.status}
			response.IncompleteDetails.Reason = tt.reason
			if err := incompleteResponseError(response); (err != nil) != tt.wantErr {
				t.Fatalf("incompleteResponseError() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFinishReasoningSlotUsesSummaryAndRawSignature(t *testing.T) {
	partial := &llm.AssistantMessage{}
	slots := map[outputSlotKey]outputSlot{}
	item := responseItem(t, `{"type":"reasoning","id":"r1","summary":[],"content":[],"status":"in_progress"}`)
	var events []llm.Event
	if !addOutputSlot(partial, slots, 0, item, func(event llm.Event) bool {
		events = append(events, event)
		return true
	}) {
		t.Fatal("addOutputSlot returned false")
	}
	done := responseItem(t, `{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"summary"}],"content":[{"type":"reasoning_text","text":"cot"}],"encrypted_content":"enc","status":"completed"}`)
	if !finishOutputSlot(partial, slots, 0, done, func(event llm.Event) bool {
		events = append(events, event)
		return true
	}) {
		t.Fatal("finishOutputSlot returned false")
	}
	thinking := partial.Content[0].(llm.ThinkingContent)
	if thinking.Thinking != "summary" || thinking.ThinkingSignature == "" {
		t.Fatalf("thinking = %#v", thinking)
	}
	if len(events) != 2 || events[1].Type != llm.EventThinkingEnd || events[1].Content != "summary" {
		t.Fatalf("events = %+v", events)
	}
}

func responseItem(t *testing.T, input string) responses.ResponseOutputItemUnion {
	t.Helper()
	var item responses.ResponseOutputItemUnion
	if err := item.UnmarshalJSON([]byte(input)); err != nil {
		t.Fatal(err)
	}
	return item
}
