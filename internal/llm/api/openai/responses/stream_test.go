package responses

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestOutputSlotsPreserveInterleavedContent(t *testing.T) {
	partial := &llm.AssistantMessage{}
	slots := map[outputSlotKey]outputSlot{}
	var events []llm.AssistantMessageEvent
	send := func(event llm.AssistantMessageEvent) bool {
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
	llm.AppendThinking(partial, 0, "plan")
	llm.AppendText(partial, 1, "answer")
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
	if len(events) != 3 || eventIndex(events[0]) != 0 || eventIndex(events[1]) != 1 || eventIndex(events[2]) != 2 {
		t.Fatalf("start events = %+v", events)
	}
}

func TestOutputSlotsSeparateMessageContentParts(t *testing.T) {
	partial := &llm.AssistantMessage{}
	slots := map[outputSlotKey]outputSlot{}
	var events []llm.AssistantMessageEvent
	send := func(event llm.AssistantMessageEvent) bool {
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
	llm.AppendText(partial, slots[outputSlotKey{outputIndex: 0}].contentIdx, "first")
	llm.AppendText(partial, slots[outputSlotKey{outputIndex: 0, contentIndex: 1}].contentIdx, "second")

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
	if len(events) != 4 || events[2].Type() != llm.EventTextEnd || events[3].Type() != llm.EventTextEnd || eventIndex(events[2]) != 0 || eventIndex(events[3]) != 1 {
		t.Fatalf("events = %+v, want independent text end events", events)
	}
}

func TestAddOutputSlotRejectsCustomTool(t *testing.T) {
	var event llm.AssistantMessageEvent
	accepted := addOutputSlot(&llm.AssistantMessage{}, map[outputSlotKey]outputSlot{}, 0,
		responseItem(t, `{"type":"custom_tool_call","id":"ct1","call_id":"call_custom","name":"apply_patch","input":"patch"}`),
		func(got llm.AssistantMessageEvent) bool {
			event = got
			return true
		})
	if accepted {
		t.Fatal("addOutputSlot accepted custom tool")
	}
	if event.Type() != llm.EventError || event.(llm.AssistantMessageErrorEvent).Err == nil {
		t.Fatalf("event = %+v, want unsupported custom tool error", event)
	}
}

func TestToolCallStartIncludesDetails(t *testing.T) {
	partial := &llm.AssistantMessage{}
	var event llm.AssistantMessageEvent
	accepted := addOutputSlot(partial, map[outputSlotKey]outputSlot{}, 0,
		responseItem(t, `{"type":"function_call","id":"f1","call_id":"call-1","name":"read","arguments":"{\"path\":\"a.txt\"}"}`),
		func(got llm.AssistantMessageEvent) bool {
			event = got
			return true
		})
	if !accepted {
		t.Fatal("addOutputSlot returned false")
	}
	if event.Type() != llm.EventToolCallStart {
		t.Fatalf("event type = %q, want toolcall_start", event.Type())
	}
	toolEvent := event.(llm.AssistantMessageToolCallStartEvent)
	if toolEvent.ToolCall.ID != "call-1" || toolEvent.ToolCall.Name != "read" || toolEvent.ToolCall.Arguments["path"] != "a.txt" {
		t.Fatalf("tool call = %#v, want call-1/read/path=a.txt", toolEvent.ToolCall)
	}
}

func TestAssistantMessageSnapshotCopiesContent(t *testing.T) {
	message := &llm.AssistantMessage{
		ResponseID: "before",
		Content:    []llm.Content{llm.TextContent{Text: "before"}},
	}
	snapshot := llm.CloneMessage(message)

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
	var events []llm.AssistantMessageEvent
	if !addOutputSlot(partial, slots, 0, item, func(event llm.AssistantMessageEvent) bool {
		events = append(events, event)
		return true
	}) {
		t.Fatal("addOutputSlot returned false")
	}
	done := responseItem(t, `{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"summary"}],"content":[{"type":"reasoning_text","text":"cot"}],"encrypted_content":"enc","status":"completed"}`)
	if !finishOutputSlot(partial, slots, 0, done, func(event llm.AssistantMessageEvent) bool {
		events = append(events, event)
		return true
	}) {
		t.Fatal("finishOutputSlot returned false")
	}
	thinking := partial.Content[0].(llm.ThinkingContent)
	if thinking.Thinking != "summary" || thinking.ThinkingSignature == "" {
		t.Fatalf("thinking = %#v", thinking)
	}
	if len(events) != 2 || events[1].Type() != llm.EventThinkingEnd || events[1].(llm.AssistantMessageThinkingEndEvent).Content != "summary" {
		t.Fatalf("events = %+v", events)
	}
}

func eventIndex(event llm.AssistantMessageEvent) int {
	switch event := event.(type) {
	case llm.AssistantMessageTextStartEvent:
		return event.ContentIndex
	case llm.AssistantMessageTextEndEvent:
		return event.ContentIndex
	case llm.AssistantMessageThinkingStartEvent:
		return event.ContentIndex
	case llm.AssistantMessageToolCallStartEvent:
		return event.ContentIndex
	case llm.AssistantMessageTextDeltaEvent:
		return event.ContentIndex
	case llm.AssistantMessageThinkingDeltaEvent:
		return event.ContentIndex
	case llm.AssistantMessageToolCallDeltaEvent:
		return event.ContentIndex
	case llm.AssistantMessageThinkingEndEvent:
		return event.ContentIndex
	case llm.AssistantMessageToolCallEndEvent:
		return event.ContentIndex
	}
	return -1
}

func responseItem(t *testing.T, input string) responses.ResponseOutputItemUnion {
	t.Helper()
	var item responses.ResponseOutputItemUnion
	if err := item.UnmarshalJSON([]byte(input)); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestClassifyAPIErrorWrapsContextOverflow(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		message string
		want    bool
	}{
		{name: "context_length_exceeded code", code: "context_length_exceeded", message: "input too long", want: true},
		{name: "max context tokens code", code: "above_model_max_context_tokens", message: "", want: true},
		{name: "maximum context length message", code: "", message: "This model's maximum context length is 128000 tokens", want: true},
		{name: "too many input tokens", code: "invalid_request_error", message: "Too many input tokens", want: true},
		{name: "loose phrase in echoed content", code: "", message: "user asked about the context window of gpt-4", want: false},
		{name: "unrelated error", code: "invalid_request_error", message: "bad json body", want: false},
		{name: "empty error", code: "", message: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyAPIError("openai", tt.code, tt.message)
			if got := llm.IsContextOverflow(err); got != tt.want {
				t.Fatalf("IsContextOverflow(%v) = %v, want %v", err, got, tt.want)
			}
			if !tt.want && strings.Contains(err.Error(), llm.ErrContextOverflow.Error()) {
				t.Errorf("unrelated error %q should not mention overflow", err)
			}
		})
	}
}

func TestClassifyStreamErrorWrapsContextOverflow(t *testing.T) {
	wrapped := errors.New("POST returned 400: context_length_exceeded")
	if err := classifyStreamError("openai: stream interrupted", wrapped); !llm.IsContextOverflow(err) {
		t.Fatalf("classifyStreamError() = %v, want overflow", err)
	}
	plain := errors.New("connection reset")
	if err := classifyStreamError("openai: stream interrupted", plain); llm.IsContextOverflow(err) {
		t.Fatalf("classifyStreamError() = %v, want non-overflow", err)
	}
}

func TestStreamEventPartialHelpersCoverAllEventTypes(t *testing.T) {
	partial := &llm.AssistantMessage{ResponseID: "response"}
	tests := []llm.AssistantMessageEvent{
		llm.AssistantMessageStartEvent{},
		llm.AssistantMessageTextStartEvent{},
		llm.AssistantMessageTextDeltaEvent{},
		llm.AssistantMessageTextEndEvent{},
		llm.AssistantMessageThinkingStartEvent{},
		llm.AssistantMessageThinkingDeltaEvent{},
		llm.AssistantMessageThinkingEndEvent{},
		llm.AssistantMessageToolCallStartEvent{},
		llm.AssistantMessageToolCallDeltaEvent{},
		llm.AssistantMessageToolCallEndEvent{},
		llm.AssistantMessageDoneEvent{},
		llm.AssistantMessageErrorEvent{},
	}
	for _, event := range tests {
		if llm.EventPartial(event) != nil {
			t.Fatalf("event %T unexpectedly has partial", event)
		}
		updated := llm.WithPartial(event, partial)
		if llm.EventPartial(updated) != partial {
			t.Fatalf("event %T did not receive partial", event)
		}
	}
}

func TestSendEventHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := make(chan llm.AssistantMessageEvent)
	if llm.SendEvent(ctx, events, llm.AssistantMessageStartEvent{}) {
		t.Fatal("sendEvent() = true for canceled context")
	}
}

func TestFinishOutputSlotHandlesMissingEmptyAndInvalidItems(t *testing.T) {
	partial := &llm.AssistantMessage{}
	slots := map[outputSlotKey]outputSlot{}
	if !finishOutputSlot(partial, slots, 99, responseItem(t, `{"type":"message","id":"m","role":"assistant","status":"completed","content":[]}`), func(llm.AssistantMessageEvent) bool { return true }) {
		t.Fatal("missing output slot should be ignored")
	}
	if !addOutputSlot(partial, slots, 0, responseItem(t, `{"type":"message","id":"m","role":"assistant","status":"in_progress","content":[]}`), func(llm.AssistantMessageEvent) bool { return true }) {
		t.Fatal("message output slot was rejected")
	}
	if !finishOutputSlot(partial, slots, 0, responseItem(t, `{"type":"message","id":"m","role":"assistant","status":"completed","content":[]}`), func(llm.AssistantMessageEvent) bool { return true }) {
		t.Fatal("empty message output was rejected")
	}
	partial = &llm.AssistantMessage{}
	slots = map[outputSlotKey]outputSlot{}
	var event llm.AssistantMessageEvent
	if !addOutputSlot(partial, slots, 0, responseItem(t, `{"type":"function_call","id":"f","call_id":"c","name":"tool","arguments":""}`), func(got llm.AssistantMessageEvent) bool { event = got; return true }) {
		t.Fatal("function output slot was rejected")
	}
	if finishOutputSlot(partial, slots, 0, responseItem(t, `{"type":"function_call","id":"f","call_id":"c","name":"tool","arguments":"{bad"}`), func(got llm.AssistantMessageEvent) bool { event = got; return true }) {
		t.Fatal("invalid function call arguments were accepted")
	}
	if event.Type() != llm.EventError {
		t.Fatalf("invalid function call event = %q, want error", event.Type())
	}
}

func TestStreamTerminalErrorEvents(t *testing.T) {
	tests := []struct {
		name string
		sse  string
		want string
	}{
		{name: "failed response", sse: `{"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"bad","message":"failed"}}}`, want: "openai: request failed: bad: failed"},
		{name: "error event", sse: `{"type":"error","code":"bad","message":"failed"}`, want: "openai: bad: failed"},
		{name: "incomplete response", sse: `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`, want: "openai: response incomplete: content_filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				writeSSE(w, tt.sse)
			}))
			defer server.Close()
			client := NewClient("test-key", server.URL+"/v1")
			events, err := client.Stream(context.Background(), testModel(), llm.Context{}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var got error
			for event := range events {
				if event.Type() == llm.EventError {
					got = event.(llm.AssistantMessageErrorEvent).Err
				}
			}
			if got == nil || got.Error() != tt.want {
				t.Fatalf("stream error = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestStreamReasoningTextDeltaFallsBackToOutputSlot(t *testing.T) {
	reasoning := `{"type":"reasoning","id":"r","summary":[],"content":[{"type":"reasoning_text","text":"cot"}],"status":"completed"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, `{"type":"response.created","response":{"id":"resp"}}`)
		writeSSE(w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"r","summary":[],"content":[],"status":"in_progress"}}`)
		writeSSE(w, `{"type":"response.reasoning_text.delta","output_index":0,"content_index":1,"delta":"cot"}`)
		writeSSE(w, `{"type":"response.output_item.done","output_index":0,"item":`+reasoning+`}`)
		writeSSE(w, `{"type":"response.completed","response":{"id":"resp","status":"completed","output":[`+reasoning+`]}}`)
	}))
	defer server.Close()

	events, err := NewClient("test-key", server.URL+"/v1").Stream(context.Background(), testModel(), llm.Context{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var delta string
	for event := range events {
		if thinking, ok := event.(llm.AssistantMessageThinkingDeltaEvent); ok {
			delta += thinking.Delta
		}
	}
	if delta != "cot" {
		t.Fatalf("thinking delta = %q, want cot", delta)
	}
}

func TestFinishEmptyMessageUsesOutputSlotContentIndex(t *testing.T) {
	partial := &llm.AssistantMessage{}
	slots := map[outputSlotKey]outputSlot{}
	var events []llm.AssistantMessageEvent
	send := func(event llm.AssistantMessageEvent) bool {
		events = append(events, event)
		return true
	}
	if !addOutputSlot(partial, slots, 0, responseItem(t, `{"type":"reasoning","id":"r","summary":[],"content":[],"status":"in_progress"}`), send) {
		t.Fatal("reasoning output slot was rejected")
	}
	if !addOutputSlot(partial, slots, 1, responseItem(t, `{"type":"message","id":"m","role":"assistant","status":"in_progress","content":[]}`), send) {
		t.Fatal("message output slot was rejected")
	}
	if !finishOutputSlot(partial, slots, 1, responseItem(t, `{"type":"message","id":"m","role":"assistant","status":"completed","content":[]}`), send) {
		t.Fatal("empty message output was rejected")
	}
	if got := eventIndex(events[len(events)-1]); got != 1 {
		t.Fatalf("empty message end index = %d, want 1", got)
	}
}

func TestStreamStopsWhenContextIsCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-w.(http.CloseNotifier).CloseNotify()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient("test-key", server.URL+"/v1")
	events, err := client.Stream(ctx, testModel(), llm.Context{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for range events {
	}
}

func TestIncompleteResponseErrorAllowsMaxOutputTokens(t *testing.T) {
	response := responses.Response{Status: responses.ResponseStatusIncomplete}
	response.IncompleteDetails.Reason = "max_output_tokens"
	if err := incompleteResponseError(response); err != nil {
		t.Fatalf("incompleteResponseError() = %v, want nil", err)
	}
}

func TestStreamRejectsUnsupportedAPI(t *testing.T) {
	client := NewClient("test-key", "")
	_, err := client.Stream(context.Background(), llm.Model{API: "chat-completions"}, llm.Context{}, llm.Options{})
	if err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("Stream() error = %v, want unsupported protocol", err)
	}
}

func TestStreamErrorEventWithoutCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, `{"type":"error","message":"failed"}`)
	}))
	defer server.Close()
	client := NewClient("test-key", server.URL+"/v1")
	events, err := client.Stream(context.Background(), testModel(), llm.Context{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for event := range events {
		if event.Type() == llm.EventError {
			got = event.(llm.AssistantMessageErrorEvent).Err.Error()
		}
	}
	if got != "openai: failed" {
		t.Fatalf("error event = %q, want openai: failed", got)
	}
}

func TestStreamCancellationErrorIsNotRequiredAfterClientCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient("test-key", "http://127.0.0.1:1/v1")
	_, err := client.Stream(ctx, testModel(), llm.Context{}, llm.Options{})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() canceled setup error = %v", err)
	}
}
