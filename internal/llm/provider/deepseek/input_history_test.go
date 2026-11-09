package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestInputHistoryOriginExcludedFromProviderRequests(t *testing.T) {
	for _, api := range []llm.API{llm.APIOpenAIResponses, llm.APIOpenAICompletions} {
		t.Run(string(api), func(t *testing.T) {
			bodies := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read request: %v", err)
				}
				bodies <- body
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			provider := New(Config{APIKey: "test-key", BaseURL: server.URL})
			origin := llm.UserInputOrigin{Generation: 24, Sequence: 91, SessionID: "private-session-id", Text: "private-original-command"}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			events, err := provider.Stream(ctx, llm.Model{ID: DefaultModel, API: api, Provider: ProviderID}, llm.Context{Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "expanded model text"}}, Origin: &origin}}}, llm.Options{})
			if err != nil {
				t.Fatal(err)
			}
			for range events {
			}
			var body []byte
			select {
			case body = <-bodies:
			case <-ctx.Done():
				t.Fatal("provider request was not received")
			}
			if !json.Valid(body) || !bytes.Contains(body, []byte("expanded model text")) {
				t.Fatalf("request = %s", body)
			}
			for _, forbidden := range [][]byte{[]byte("Origin"), []byte("origin"), []byte(origin.Text), []byte(origin.SessionID), []byte("Generation"), []byte("Sequence")} {
				if bytes.Contains(body, forbidden) {
					t.Fatalf("provider leaked %q: %s", forbidden, body)
				}
			}
		})
	}
}
