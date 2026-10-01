package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestStreamReplaysReasoningItems(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		model         string
		reasoning     string
		streamType    string
		streamDelta   string
		finalThinking string
	}{
		{
			name:          "OpenAI summary with encrypted reasoning",
			provider:      "openai",
			model:         "gpt-5.6-sol",
			reasoning:     `{"type":"reasoning","id":"r_openai","summary":[{"type":"summary_text","text":"brief summary"}],"content":[],"encrypted_content":"opaque-signature","status":"completed"}`,
			streamType:    "response.reasoning_summary_text.delta",
			streamDelta:   "brief summary",
			finalThinking: "brief summary",
		},
		{
			name:          "DeepSeek plaintext reasoning",
			provider:      "deepseek",
			model:         "deepseek-flash",
			reasoning:     `{"type":"reasoning","id":"r_deepseek","summary":[],"content":[{"type":"reasoning_text","text":"full plaintext reasoning"}],"status":"completed"}`,
			streamType:    "response.reasoning_text.delta",
			streamDelta:   "full plaintext reasoning",
			finalThinking: "full plaintext reasoning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
					http.Error(w, "unexpected endpoint", http.StatusNotFound)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, "invalid request JSON", http.StatusBadRequest)
					return
				}
				requests = append(requests, body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				if len(requests) == 1 {
					writeReasoningResponse(w, tt.reasoning, tt.streamType, tt.streamDelta)
					return
				}
				writeCompletedResponse(w, "resp_second", []string{
					`{"type":"message","id":"msg_second","role":"assistant","status":"completed","content":[{"type":"output_text","text":"second answer","annotations":[]}]}`,
				})
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL+"/v1")
			model := llm.Model{
				ID: tt.model, Name: tt.model, Provider: tt.provider,
				API: llm.APIOpenAIResponses, Reasoning: true,
				ReasoningEffort: llm.ReasoningEffortMedium,
			}
			first := collectResponse(t, client, model, llm.Context{
				Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "Give a short answer to 1+1."}}}},
			}, true)
			thinking, ok := findThinking(first.Content)
			if !ok || thinking.Thinking != tt.finalThinking || thinking.ThinkingSignature == "" {
				t.Fatalf("first response thinking = %#v, want text %q and a saved reasoning item", thinking, tt.finalThinking)
			}

			second := collectResponse(t, client, model, llm.Context{
				Messages: []llm.Message{
					llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "Give a short answer to 1+1."}}},
					*first,
					llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "Now state the result."}}},
				},
			}, false)
			if _, ok := findText(second.Content, "second answer"); !ok {
				t.Fatalf("second response content = %#v, want second answer", second.Content)
			}
			if len(requests) != 2 {
				t.Fatalf("request count = %d, want 2", len(requests))
			}
			input, ok := requests[1]["input"].([]any)
			if !ok {
				t.Fatalf("second request input has type %T", requests[1]["input"])
			}
			gotReasoning, ok := findReasoningItem(input)
			if !ok {
				t.Fatalf("second request did not replay a reasoning item: %#v", input)
			}
			wantReasoning := decodeObject(t, tt.reasoning)
			if !reflect.DeepEqual(gotReasoning, wantReasoning) {
				t.Fatalf("replayed reasoning item = %#v, want %#v", gotReasoning, wantReasoning)
			}
			requestReasoning, ok := requests[0]["reasoning"].(map[string]any)
			if !ok || requestReasoning["effort"] != "medium" || requestReasoning["summary"] != "auto" {
				t.Fatalf("first request reasoning config = %#v", requests[0]["reasoning"])
			}
			if got, ok := requests[0]["include"].([]any); !ok || !reflect.DeepEqual(got, []any{"reasoning.encrypted_content"}) {
				t.Fatalf("first request include = %#v", requests[0]["include"])
			}
		})
	}
}

func collectResponse(t *testing.T, client *Client, model llm.Model, input llm.Context, wantThinkingDelta bool) *llm.AssistantMessage {
	t.Helper()
	events, err := client.Stream(context.Background(), model, input, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var message *llm.AssistantMessage
	var gotThinkingDelta bool
	for event := range events {
		switch event.Type() {
		case llm.EventThinkingDelta:
			gotThinkingDelta = true
		case llm.EventError:
			t.Fatal(event.(llm.AssistantMessageErrorEvent).Err)
		case llm.EventDone:
			message = event.(llm.AssistantMessageDoneEvent).Message
		}
	}
	if wantThinkingDelta && !gotThinkingDelta {
		t.Fatal("stream did not emit a thinking delta")
	}
	if message == nil {
		t.Fatal("stream did not emit a completed assistant message")
	}
	return message
}

func writeReasoningResponse(w http.ResponseWriter, reasoning, deltaType, delta string) {
	writeSSE(w, `{"type":"response.created","response":{"id":"resp_first"}}`)
	writeSSE(w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"r_in_progress","summary":[],"content":[],"status":"in_progress"}}`)
	writeSSE(w, fmt.Sprintf(`{"type":%q,"output_index":0,"delta":%q}`, deltaType, delta))
	writeSSE(w, `{"type":"response.output_item.done","output_index":0,"item":`+reasoning+`}`)
	writeSSE(w, `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_first","role":"assistant","status":"in_progress","content":[]}}`)
	writeSSE(w, `{"type":"response.output_text.delta","output_index":1,"delta":"first answer"}`)
	message := `{"type":"message","id":"msg_first","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first answer","annotations":[]}]}`
	writeSSE(w, `{"type":"response.output_item.done","output_index":1,"item":`+message+`}`)
	writeCompletedResponse(w, "resp_first", []string{reasoning, message})
}

func writeCompletedResponse(w http.ResponseWriter, id string, output []string) {
	writeSSE(w, `{"type":"response.completed","response":{"id":`+mustJSON(id)+`,"object":"response","created_at":1,"status":"completed","output":[`+strings.Join(output, ",")+`]}}`)
}

func writeSSE(w http.ResponseWriter, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType(data), data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func eventType(data string) string {
	var event struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(data), &event)
	return event.Type
}

func mustJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func findReasoningItem(items []any) (map[string]any, bool) {
	for _, value := range items {
		item, ok := value.(map[string]any)
		if ok && item["type"] == "reasoning" {
			return item, true
		}
	}
	return nil, false
}

func findThinking(content []llm.Content) (llm.ThinkingContent, bool) {
	for _, value := range content {
		if thinking, ok := value.(llm.ThinkingContent); ok {
			return thinking, true
		}
	}
	return llm.ThinkingContent{}, false
}

func findText(content []llm.Content, text string) (llm.TextContent, bool) {
	for _, value := range content {
		if got, ok := value.(llm.TextContent); ok && got.Text == text {
			return got, true
		}
	}
	return llm.TextContent{}, false
}
