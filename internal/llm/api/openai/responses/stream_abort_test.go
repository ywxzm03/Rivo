package responses

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 阻塞式服务端：发送一个 delta 后挂住，直到请求被取消
func blockingStreamServer(t *testing.T, clientGone chan<- struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.(http.Flusher).Flush()
		writeSSE(w, `{"type":"response.created","response":{"id":"resp_abort"}}`)
		writeSSE(w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","status":"in_progress","content":[{"type":"output_text","text":""}]}}`)
		writeSSE(w, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}`)
		<-r.Context().Done() // 挂住直到客户端断开
		close(clientGone)
	}))
	return server
}

// 收集事件直到 channel 关闭；返回收到的事件与 goroutine 退出确认
func drainAbortEvents(t *testing.T, events <-chan llm.AssistantMessageEvent) []llm.AssistantMessageEvent {
	t.Helper()
	var collected []llm.AssistantMessageEvent
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

// 取消时正在阻塞读流：终止事件必须送达且标记 aborted，并携带已收到的 partial
func TestStreamCancelDeliversAbortedErrorEvent(t *testing.T) {
	clientGone := make(chan struct{})
	server := blockingStreamServer(t, clientGone)
	defer server.Close()

	client := NewClient("test-key", server.URL+"/v1")
	model := llm.Model{ID: "m", Provider: "deepseek", API: llm.APIOpenAIResponses}
	ctx, cancel := context.WithCancel(context.Background())
	events, err := client.Stream(ctx, model, llm.Context{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}

	var gotDelta bool
	done := make(chan []llm.AssistantMessageEvent, 1)
	go func() { done <- drainAbortEvents(t, events) }()
	time.Sleep(100 * time.Millisecond) // 等 delta 事件送达
	cancel()
	got := <-done
	clientGoneClosed := false
	select {
	case <-clientGone:
		clientGoneClosed = true
	case <-time.After(2 * time.Second):
	}
	if !clientGoneClosed {
		t.Fatal("server did not observe client disconnect")
	}
	for _, event := range got {
		switch ev := event.(type) {
		case llm.AssistantMessageTextDeltaEvent:
			gotDelta = true
		case llm.AssistantMessageErrorEvent:
			if ev.Reason != llm.StopReasonAborted {
				t.Fatalf("error event reason = %q, want aborted", ev.Reason)
			}
			if ev.Partial == nil {
				t.Fatal("abort error event should carry partial content")
			}
			text, _ := ev.Partial.Content[0].(llm.TextContent)
			if text.Text != "partial" {
				t.Fatalf("partial text = %q, want preserved partial", text.Text)
			}
			if !strings.Contains(ev.Err.Error(), "abort") && !strings.Contains(ev.Err.Error(), "cancel") {
				t.Fatalf("abort error should preserve cause: %v", ev.Err)
			}
		}
	}
	if !gotDelta {
		t.Fatal("did not receive text delta before cancel")
	}
	if len(got) < 2 {
		t.Fatalf("expected delta and error events, got %d", len(got))
	}
}

// 取消发生在请求建立之前：终止事件同样必须送达（NewStreaming 立即失败路径）
func TestStreamCancelBeforeConnectDeliversAborted(t *testing.T) {
	var requests int
	unblock := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		<-unblock // 请求挂起，不返回
	}))
	// Go defer LIFO：后注册先执行，因此 close(unblock) 必须注册在 server.Close() 之后
	defer server.Close()
	defer close(unblock)

	client := NewClient("test-key", server.URL+"/v1")
	model := llm.Model{ID: "m", Provider: "deepseek", API: llm.APIOpenAIResponses}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Stream 内部 NewStreaming 同步建连；取消后才会返回带错误的 stream
	type streamResult struct {
		events <-chan llm.AssistantMessageEvent
		err    error
	}
	resultCh := make(chan streamResult, 1)
	go func() {
		events, err := client.Stream(ctx, model, llm.Context{}, llm.Options{})
		resultCh <- streamResult{events, err}
	}()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	var events <-chan llm.AssistantMessageEvent
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		events = result.events
	case <-time.After(10 * time.Second):
		t.Fatal("Stream did not return after cancellation")
	}
	var gotError bool
	for event := range events {
		if ev, ok := event.(llm.AssistantMessageErrorEvent); ok {
			gotError = true
			if ev.Reason != llm.StopReasonAborted {
				t.Fatalf("error event reason = %q, want aborted", ev.Reason)
			}
			if !errors.Is(ev.Err, context.Canceled) {
				t.Fatalf("abort error should wrap context.Canceled: %v", ev.Err)
			}
		}
	}
	if !gotError {
		t.Fatal("cancelled stream must still deliver an error event with reason=aborted")
	}
}

// 正常完成路径不受阻塞发送影响
func TestStreamNormalCompletionStillWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeCompletedResponse(w, "resp_ok", []string{
			`{"type":"message","id":"msg_ok","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[]}]}`,
		})
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL+"/v1")
	model := llm.Model{ID: "m", Provider: "deepseek", API: llm.APIOpenAIResponses}
	message := collectResponse(t, client, model, llm.Context{}, false)
	if text, ok := message.Content[0].(llm.TextContent); !ok || text.Text != "done" {
		t.Fatalf("message content = %#v", message.Content)
	}
}
