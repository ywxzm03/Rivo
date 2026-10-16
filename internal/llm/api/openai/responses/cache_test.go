package responses

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

func TestBuildParamsPromptCacheFields(t *testing.T) {
	tests := []struct {
		name  string
		model llm.Model
		opts  llm.Options
		want  string
	}{
		{
			name:  "OpenAI GPT-5.6 uses session key and TTL",
			model: llm.Model{ID: "gpt-5.6-sol", Provider: "openai", API: llm.APIOpenAIResponses},
			opts:  llm.Options{SessionID: "123e4567-e89b-12d3-a456-426614174000"},
			want:  `{"model":"gpt-5.6-sol","prompt_cache_key":"123e4567-e89b-12d3-a456-426614174000","prompt_cache_options":{"ttl":"30m"}}`,
		},
		{
			name:  "OpenAI GPT-6 uses TTL",
			model: llm.Model{ID: "gpt-6-luna", Provider: "openai", API: llm.APIOpenAIResponses},
			opts:  llm.Options{SessionID: "session-6"},
			want:  `{"model":"gpt-6-luna","prompt_cache_key":"session-6","prompt_cache_options":{"ttl":"30m"}}`,
		},
		{
			name:  "older OpenAI model only uses session key",
			model: llm.Model{ID: "gpt-5", Provider: "openai", API: llm.APIOpenAIResponses},
			opts:  llm.Options{SessionID: "session-old"},
			want:  `{"model":"gpt-5","prompt_cache_key":"session-old"}`,
		},
		{
			name:  "DeepSeek Responses omits cache fields",
			model: llm.Model{ID: "deepseek-v4-pro", Provider: "deepseek", API: llm.APIOpenAIResponses},
			opts:  llm.Options{SessionID: "session-deepseek"},
			want:  `{"model":"deepseek-v4-pro"}`,
		},
		{
			name:  "OpenAI GPT-5.6 omits cache fields without session key",
			model: llm.Model{ID: "gpt-5.6-terra", Provider: "openai", API: llm.APIOpenAIResponses},
			want:  `{"model":"gpt-5.6-terra"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := BuildParams(tt.model, llm.Context{}, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			assertWire(t, params, tt.want)
		})
	}
}

func TestBuildParamsRejectsPromptCacheRetentionSamplingParam(t *testing.T) {
	model := llm.Model{
		ID:       "gpt-5.6-sol",
		Provider: "openai",
		API:      llm.APIOpenAIResponses,
		SamplingParams: map[string]any{
			"prompt_cache_retention": "24h",
			"prompt_cache_options":   map[string]any{"ttl": "24h"},
		},
	}
	params, err := BuildParams(model, llm.Context{}, llm.Options{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	assertWire(t, params, `{"model":"gpt-5.6-sol","prompt_cache_key":"session-1","prompt_cache_options":{"ttl":"30m"}}`)
}

func TestStreamAddsOpenAICacheHeadersOnly(t *testing.T) {
	tests := []struct {
		name       string
		model      llm.Model
		sessionID  string
		wantHeader bool
		wantKey    bool
	}{
		{
			name:       "OpenAI",
			model:      llm.Model{ID: "gpt-5.6-sol", Provider: "openai", API: llm.APIOpenAIResponses},
			sessionID:  "session-openai",
			wantHeader: true,
			wantKey:    true,
		},
		{
			name:       "DeepSeek",
			model:      llm.Model{ID: "deepseek-v4-pro", Provider: "deepseek", API: llm.APIOpenAIResponses},
			sessionID:  "session-deepseek",
			wantHeader: false,
			wantKey:    false,
		},
		{
			name:       "OpenAI without session",
			model:      llm.Model{ID: "gpt-5.6-sol", Provider: "openai", API: llm.APIOpenAIResponses},
			wantHeader: false,
			wantKey:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
				_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cache\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL+"/v1")
			events, err := client.Stream(context.Background(), tt.model, llm.Context{}, llm.Options{SessionID: tt.sessionID})
			if err != nil {
				t.Fatal(err)
			}
			for event := range events {
				if event.Type() == llm.EventError {
					t.Fatalf("stream error: %v", event.(llm.AssistantMessageErrorEvent).Err)
				}
			}

			for _, name := range []string{"session_id", "x-client-request-id"} {
				got := gotHeaders.Get(name)
				if tt.wantHeader && got != tt.sessionID {
					t.Fatalf("%s = %q, want %q", name, got, tt.sessionID)
				}
				if !tt.wantHeader && got != "" {
					t.Fatalf("%s = %q, want omitted", name, got)
				}
			}
			_, hasKey := gotBody["prompt_cache_key"]
			if hasKey != tt.wantKey {
				t.Fatalf("prompt_cache_key present = %t, want %t; body = %#v", hasKey, tt.wantKey, gotBody)
			}
		})
	}
}
