package app

import (
	"context"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
)

// sendTest 模拟流层的取消感知发送；取消时静默关闭以覆盖 agent 兜底路径
func sendTest(ctx context.Context, events chan<- llm.AssistantMessageEvent, event llm.AssistantMessageEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// Escape 取消后 app.Run 以 aborted 消息收场且不返回错误；错误消息不污染下一次请求
func TestAppCancelSettlesWithAbortedAndReplaysCleanly(t *testing.T) {
	cfg := configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel)
	spec := cfg.registry[deepseek.ProviderID]
	spec.new = func(string, string) ([]llm.Model, llm.StreamFn) {
		streamFn := func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
			events := make(chan llm.AssistantMessageEvent)
			go func() {
				defer close(events)
				partial := &llm.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID}
				partial.Content = append(partial.Content, llm.TextContent{Text: "partial answer"})
				if !sendTest(ctx, events, llm.AssistantMessageStartEvent{Partial: partial}) {
					return
				}
				select {
				case events <- llm.AssistantMessageTextDeltaEvent{ContentIndex: 0, Delta: " more", Partial: partial}:
				case <-ctx.Done():
					// 发送 delta 途中被取消：静默关闭，覆盖 agent 的 aborted 兜底路径
					return
				}
				<-ctx.Done()
			}()
			return events, nil
		}
		return []llm.Model{spec.models[0]}, streamFn
	}
	cfg.registry[deepseek.ProviderID] = spec

	a, err := NewWithSessionDir(cfg, t.TempDir(), func(event llm.AgentEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	message, runErr := a.Run(ctx, "hello")
	if runErr != nil {
		t.Fatalf("cancel should settle via aborted, got error: %v", runErr)
	}
	if message == nil || message.StopReason != llm.StopReasonAborted {
		t.Fatalf("stopReason = %+v, want aborted", message)
	}
	if message.ErrorMessage != "Operation aborted" {
		t.Fatalf("errorMessage = %q", message.ErrorMessage)
	}
	text := ""
	for _, content := range message.Content {
		if textContent, ok := content.(llm.TextContent); ok {
			text += textContent.Text
		}
	}
	if text == "" {
		t.Fatal("aborted message should preserve partial content")
	}

	// 下一次请求不得携带 aborted 消息（contextForModel 过滤）
	next := func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		messageCount := 0
		for _, message := range c.Messages {
			if _, ok := message.(llm.SystemMessage); !ok {
				messageCount++
			}
		}
		if messageCount != 2 {
			t.Fatalf("replay messages = %d non-system messages, want user + new user only", messageCount)
		}
		for _, m := range c.Messages {
			if assistant, ok := m.(llm.AssistantMessage); ok {
				if assistant.StopReason == llm.StopReasonAborted || assistant.StopReason == llm.StopReasonError {
					t.Fatalf("failed assistant leaked into replay: %+v", assistant)
				}
			}
		}
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}}}
		close(events)
		return events, nil
	}
	if err := a.SetModel(deepseek.ProviderID, deepseek.DefaultModel); err == nil {
		// SetModel 会覆盖 StreamFn；改为直接注入测试流
		a.agent.StreamFn = next
	} else {
		a.agent.StreamFn = next
	}
	if _, err := a.Run(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
}
