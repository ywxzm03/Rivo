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
	if done.Usage.InputTokens != 2 || done.Usage.OutputTokens != 1 || done.Usage.CacheReadTokens != 7 || done.Usage.CacheWriteTokens != 1 || done.Usage.TotalTokens != 11 {
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

func TestStreamUsesChoiceUsageFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			`data: {"id":"choice-usage","object":"chat.completion.chunk","created":1,"model":"gpt-5.6-sol","choices":[{"index":0,"delta":{"content":"ok"},"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4}}}]}`+"\n\n",
			`data: {"id":"choice-usage","object":"chat.completion.chunk","created":1,"model":"gpt-5.6-sol","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n",
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
		if value, ok := event.(llm.AssistantMessageDoneEvent); ok {
			done = value.Message
		}
	}
	if done == nil || done.Usage.InputTokens != 6 || done.Usage.CacheReadTokens != 4 || done.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, want choice fallback", done.Usage)
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

func TestUsageFromRawRejectsEmptyUsageAndIncludesNestedCacheWrite1H(t *testing.T) {
	if usage, ok := usageFromRaw(map[string]any{"usage": map[string]any{}}); ok || usage != (llm.Usage{}) {
		t.Fatalf("empty usage = %+v, %v; want no usage", usage, ok)
	}

	usage, ok := usageFromRaw(map[string]any{"usage": map[string]any{
		"prompt_tokens":     10.0,
		"completion_tokens": 2.0,
		"prompt_tokens_details": map[string]any{
			"cache_write_1h_tokens": 3.0,
		},
	}})
	if !ok {
		t.Fatal("nested cache write usage was rejected")
	}
	want := llm.Usage{InputTokens: 7, OutputTokens: 2, CacheWriteTokens: 3, CacheWrite1HTokens: 3, TotalTokens: 12}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}
