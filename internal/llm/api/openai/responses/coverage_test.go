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

func TestAssistantMessageIDVariants(t *testing.T) {
	if got := assistantMessageID(3, 0); got != "msg_rivo_3" {
		t.Fatalf("first assistant message ID = %q", got)
	}
	if got := assistantMessageID(3, 2); got != "msg_rivo_3_2" {
		t.Fatalf("later assistant message ID = %q", got)
	}
}

func TestBuildInputItemsRejectsUnknownMessage(t *testing.T) {
	if _, err := BuildInputItems(llm.Context{Messages: []llm.Message{nil}}); err == nil {
		t.Fatal("BuildInputItems() error = nil, want unknown message error")
	}
}

func TestBuildParamsRejectsInvalidReasoningSignature(t *testing.T) {
	model := testModel()
	model.Reasoning = true
	_, err := BuildParams(model, llm.Context{Messages: []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{
			llm.ThinkingContent{ThinkingSignature: "not-json"},
		}},
	}}, llm.Options{})
	if err == nil || !strings.Contains(err.Error(), "解析 reasoning item") {
		t.Fatalf("BuildParams() error = %v, want reasoning item parse error", err)
	}
}

func TestBuildAssistantMessageFallbackAndTerminalReasons(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		wantReason llm.StopReason
		wantText   string
	}{
		{
			name:       "reasoning content fallback",
			response:   `{"id":"r","status":"completed","output":[{"type":"reasoning","id":"reasoning-1","summary":[],"content":[{"type":"reasoning_text","text":"cot"}],"status":"completed"}]}`,
			wantReason: llm.StopReasonStop,
			wantText:   "cot",
		},
		{
			name:       "refusal text",
			response:   `{"id":"r","status":"completed","output":[{"type":"message","id":"message-1","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"cannot"}]}]}`,
			wantReason: llm.StopReasonStop,
			wantText:   "cannot",
		},
		{
			name:       "content filter",
			response:   `{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`,
			wantReason: llm.StopReasonError,
		},
		{
			name:       "failed",
			response:   `{"id":"r","status":"failed","output":[]}`,
			wantReason: llm.StopReasonError,
		},
		{
			name:       "cancelled",
			response:   `{"id":"r","status":"cancelled","output":[]}`,
			wantReason: llm.StopReasonError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message, _, err := BuildAssistantMessage(responseFromJSON(t, tt.response))
			if err != nil {
				t.Fatal(err)
			}
			if message.StopReason != tt.wantReason {
				t.Fatalf("stop reason = %q, want %q", message.StopReason, tt.wantReason)
			}
			if tt.wantText != "" {
				var gotText string
				switch content := message.Content[0].(type) {
				case llm.ThinkingContent:
					gotText = content.Thinking
				case llm.TextContent:
					gotText = content.Text
				}
				if gotText != tt.wantText {
					t.Fatalf("thinking = %#v, want %q", message.Content, tt.wantText)
				}
			}
		})
	}
}

func TestBuildAssistantMessageRejectsUnsupportedOutput(t *testing.T) {
	response := responseFromJSON(t, `{"id":"r","status":"completed","output":[{"type":"web_search_call","id":"search-1","status":"completed"}]}`)
	if _, _, err := BuildAssistantMessage(response); err == nil {
		t.Fatal("BuildAssistantMessage() error = nil, want unsupported output error")
	}
}

func TestAssistantMessageItemsRejectsUnmarshalableArguments(t *testing.T) {
	_, err := assistantMessageItems(0, llm.AssistantMessage{Content: []llm.Content{
		llm.ToolCall{ID: "call", Name: "tool", Arguments: map[string]any{"bad": func() {}}},
	}})
	if err == nil {
		t.Fatal("assistantMessageItems() error = nil, want marshal error")
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
		if eventPartial(event) != nil {
			t.Fatalf("event %T unexpectedly has partial", event)
		}
		updated := withEventPartial(event, partial)
		if eventPartial(updated) != partial {
			t.Fatalf("event %T did not receive partial", event)
		}
	}
}

func TestSendEventHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := make(chan llm.AssistantMessageEvent)
	if sendEvent(ctx, events, llm.AssistantMessageStartEvent{}) {
		t.Fatal("sendEvent() = true for canceled context")
	}
}

func TestAPIErrorFormatsOptionalCode(t *testing.T) {
	if got := apiError("openai", "", "failed").Error(); got != "openai: failed" {
		t.Fatalf("error without code = %q", got)
	}
	if got := apiError("openai", "bad_request", "failed").Error(); got != "openai: bad_request: failed" {
		t.Fatalf("error with code = %q", got)
	}
}

func TestContentPartSlotRejectsUnknownAndDeduplicates(t *testing.T) {
	partial := &llm.AssistantMessage{}
	slots := map[outputSlotKey]outputSlot{}
	var events []llm.AssistantMessageEvent
	send := func(event llm.AssistantMessageEvent) bool {
		events = append(events, event)
		return true
	}
	if addContentPartSlot(partial, slots, 0, 0, "image", send) {
		t.Fatal("unknown content part was accepted")
	}
	if len(events) != 1 || events[0].Type() != llm.EventError {
		t.Fatalf("unknown content part events = %#v", events)
	}
	if !addContentPartSlot(partial, slots, 0, 0, "output_text", send) {
		t.Fatal("output text content part was rejected")
	}
	contentCount := len(partial.Content)
	if !addContentPartSlot(partial, slots, 0, 0, "output_text", send) {
		t.Fatal("duplicate content part was rejected")
	}
	if len(partial.Content) != contentCount {
		t.Fatalf("duplicate content part changed content length to %d", len(partial.Content))
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
		{name: "failed response", sse: `{"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"bad","message":"failed"}}}`, want: "openai: 请求失败: bad: failed"},
		{name: "error event", sse: `{"type":"error","code":"bad","message":"failed"}`, want: "openai: bad: failed"},
		{name: "incomplete response", sse: `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`, want: "openai: 响应未完成: content_filter"},
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
	if err == nil || !strings.Contains(err.Error(), "暂不支持协议") {
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
