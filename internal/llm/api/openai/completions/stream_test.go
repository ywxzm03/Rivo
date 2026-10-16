package completions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
			"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-5.6-sol\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"total_tokens\":11,\"prompt_tokens_details\":{\"cached_tokens\":7,\"cache_write_tokens\":1}}}\n\n",
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
	if done.Usage.InputTokens != 10 || done.Usage.OutputTokens != 1 {
		t.Fatalf("usage = %+v", done.Usage)
	}
	if gotHeaders.Get("session_id") != "session-1" || gotHeaders.Get("x-client-request-id") != "session-1" || gotHeaders.Get("x-session-affinity") != "session-1" {
		t.Fatalf("cache headers = %#v", gotHeaders)
	}
	if gotBody["prompt_cache_options"] == nil || gotBody["prompt_cache_key"] != "session-1" {
		t.Fatalf("cache body = %#v", gotBody)
	}
}

func TestStreamPreservesLengthWithToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			`data: {"id":"chatcmpl-length","object":"chat.completion.chunk","created":1,"model":"gpt-5.6-sol","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"write","arguments":"{\"path\":\"x\""}}]},"finish_reason":null}]}`+"\n\n",
			`data: {"id":"chatcmpl-length","object":"chat.completion.chunk","created":1,"model":"gpt-5.6-sol","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"length"}]}`+"\n\n",
			"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL+"/v1")
	model := llm.Model{ID: "gpt-5.6-sol", Provider: "openai", API: llm.APIOpenAICompletions}
	events, err := client.Stream(context.Background(), model, llm.Context{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var done *llm.AssistantMessage
	for event := range events {
		switch value := event.(type) {
		case llm.AssistantMessageDoneEvent:
			done = value.Message
		case llm.AssistantMessageErrorEvent:
			t.Fatalf("stream error: %v", value.Err)
		}
	}
	if done == nil || done.StopReason != llm.StopReasonLength {
		t.Fatalf("done = %#v, want length stop reason", done)
	}
	if len(done.Content) != 1 {
		t.Fatalf("content = %#v", done.Content)
	}
	call, ok := done.Content[0].(llm.ToolCall)
	if !ok || call.Arguments["path"] != "x" {
		t.Fatalf("tool call = %#v", done.Content[0])
	}
}

func TestStreamStartCancellationPreservesCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	client := NewClient("test-key", server.URL+"/v1")
	model := llm.Model{ID: "gpt-5.6-sol", Provider: "openai", API: llm.APIOpenAICompletions}
	events, err := client.Stream(ctx, model, llm.Context{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for event := range events {
		if value, ok := event.(llm.AssistantMessageErrorEvent); ok {
			streamErr = value.Err
		}
	}
	if !errors.Is(streamErr, context.DeadlineExceeded) {
		t.Fatalf("stream error = %v, want deadline exceeded", streamErr)
	}
}
