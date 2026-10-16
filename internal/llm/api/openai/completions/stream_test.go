package completions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestStreamConvertsTextAndCacheHeaders(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(body, &gotBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-5.6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n",
			"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-5.6-sol\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n",
			"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL+"/v1")
	model := llm.Model{ID: "gpt-5.6-sol", Provider: "openai", API: llm.APIOpenAICompletions}
	events, err := client.Stream(context.Background(), model, llm.Context{}, llm.Options{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	var done *llm.AssistantMessage
	for event := range events {
		if value, ok := event.(llm.AssistantMessageDoneEvent); ok {
			done = value.Message
		}
		if value, ok := event.(llm.AssistantMessageErrorEvent); ok {
			t.Fatalf("stream error: %v", value.Err)
		}
	}
	if done == nil || len(done.Content) != 1 || done.Content[0].(llm.TextContent).Text != "hello" {
		t.Fatalf("done = %#v", done)
	}
	if gotHeaders.Get("session_id") != "session-1" || gotHeaders.Get("x-client-request-id") != "session-1" || gotHeaders.Get("x-session-affinity") != "session-1" {
		t.Fatalf("cache headers = %#v", gotHeaders)
	}
	if gotBody["prompt_cache_options"] == nil || gotBody["prompt_cache_key"] != "session-1" {
		t.Fatalf("cache body = %#v", gotBody)
	}
}
