package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/api/openai/responses"
)

func TestSinkFailureDrainsResponsesAndClosesConnection(t *testing.T) {
	clientGone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, data := range []string{
			`{"type":"response.created","response":{"id":"r1"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","status":"in_progress","content":[]}}`,
			`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
		close(clientGone)
	}))
	defer server.Close()
	sentinel := errors.New("output failed")
	client := responses.NewClient("test", server.URL)
	a := NewAgent(llm.AgentConfig{Model: llm.Model{ID: "test", API: llm.APIOpenAIResponses}}, llm.Context{}, client.Stream, func(event llm.AgentEvent) error {
		if _, ok := event.(llm.MessageUpdateEvent); ok {
			return sentinel
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := a.Run(ctx, "hello"); finished <- err }()
	select {
	case err := <-finished:
		if !errors.Is(err, sentinel) {
			t.Fatalf("Run error = %v, want original sink error", err)
		}
	case <-ctx.Done():
		t.Fatal("consumer returned without joining Responses producer")
	}
	select {
	case <-clientGone:
	case <-ctx.Done():
		t.Fatal("HTTP connection was not released")
	}
}

func TestSilentEOFIsFailureWithoutCancellation(t *testing.T) {
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent)
		close(ch)
		return ch, nil
	}, nil)
	message, err := a.Run(context.Background(), "hello")
	if err == nil || message == nil || message.StopReason != llm.StopReasonError {
		t.Fatalf("silent EOF = %+v, %v; want model failure", message, err)
	}
}
