package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// silentCancelStreamFn 返回一个立即关闭的通道，模拟取消时 error 事件丢失的旧竞态
func silentCancelStreamFn() llm.StreamFn {
	return func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		events := make(chan llm.AssistantMessageEvent)
		go func() {
			<-ctx.Done()
			close(events)
		}()
		return events, nil
	}
}

// collectEvents 记录 agent 发出的全部事件
func collectEvents() (*[]llm.AgentEvent, llm.AgentEventSink) {
	events := &[]llm.AgentEvent{}
	return events, func(event llm.AgentEvent) error {
		*events = append(*events, event)
		return nil
	}
}

// 取消后即使流层静默关闭（防御路径），agent 也必须以 aborted assistant 收场并发出完整事件序列
func TestRunAbortSilentStreamClosesWithAborted(t *testing.T) {
	events, sink := collectEvents()
	a := New(Options{Config: llm.AgentConfig{Model: llm.Model{ID: "m1"}}, Context: llm.Context{}, Stream: silentCancelStreamFn(), Events: sink})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	message, err := a.Run(ctx, "hello")
	if err != nil {
		t.Fatalf("abort should settle via events, not error: %v", err)
	}
	if message == nil || message.StopReason != llm.StopReasonAborted {
		t.Fatalf("message stopReason = %+v, want aborted", message)
	}
	if message.ErrorMessage != "Operation aborted" {
		t.Fatalf("errorMessage = %q, want Operation aborted", message.ErrorMessage)
	}
	assertAbortedEventSequence(t, *events)
}

// 流层显式发出 Reason=aborted 的错误事件时，agent 以 aborted 收场并保留 partial 内容
func TestRunAbortErrorEventKeepsPartial(t *testing.T) {
	events, sink := collectEvents()
	streamFn := func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		events2 := make(chan llm.AssistantMessageEvent, 2)
		partial := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "partial answer"}}, Usage: llm.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}
		events2 <- llm.AssistantMessageTextDeltaEvent{ContentIndex: 0, Delta: "partial answer", Partial: partial}
		events2 <- llm.AssistantMessageErrorEvent{Reason: llm.StopReasonAborted, Err: context.Canceled, Partial: partial}
		close(events2)
		return events2, nil
	}
	a := New(Options{Config: llm.AgentConfig{Model: llm.Model{ID: "m1"}}, Context: llm.Context{}, Stream: streamFn, Events: sink})
	message, err := a.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("abort should settle via events: %v", err)
	}
	if message == nil || message.StopReason != llm.StopReasonAborted {
		t.Fatalf("stopReason = %+v, want aborted", message)
	}
	if text, ok := message.Content[0].(llm.TextContent); !ok || text.Text != "partial answer" {
		t.Fatalf("partial content lost: %+v", message.Content)
	}
	if message.Usage.TotalTokens != 12 {
		t.Fatalf("partial usage lost: %+v", message.Usage)
	}
	assertAbortedEventSequence(t, *events)
}

// 非取消错误仍以 error 收场，且 ErrorMessage 记录原始错误
func TestRunModelErrorKeepsErrorReason(t *testing.T) {
	events, sink := collectEvents()
	streamFn := func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageErrorEvent{Reason: llm.StopReasonError, Err: errors.New("boom")}
		close(ch)
		return ch, nil
	}
	a := New(Options{Config: llm.AgentConfig{Model: llm.Model{ID: "m1"}}, Context: llm.Context{}, Stream: streamFn, Events: sink})
	message, err := a.Run(context.Background(), "hello")
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
	if message == nil || message.StopReason != llm.StopReasonError {
		t.Fatalf("stopReason = %+v, want error", message)
	}
	if message.ErrorMessage != "boom" {
		t.Fatalf("errorMessage = %q, want boom", message.ErrorMessage)
	}
	assertErrorEventSequence(t, *events)
}

// 请求投影同时过滤 error 与 aborted 的 assistant
func TestContextForModelFiltersAborted(t *testing.T) {
	agentCtx := llm.Context{Messages: []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "u1"}}},
		llm.AssistantMessage{StopReason: llm.StopReasonError, Content: []llm.Content{llm.TextContent{Text: "err"}}},
		llm.AssistantMessage{StopReason: llm.StopReasonAborted, Content: []llm.Content{llm.TextContent{Text: "aborted partial"}}},
		llm.AssistantMessage{StopReason: llm.StopReasonStop, Content: []llm.Content{llm.TextContent{Text: "ok"}}},
	}}
	filtered := contextForModel(agentCtx)
	if len(filtered.Messages) != 2 {
		t.Fatalf("filtered messages = %d, want 2", len(filtered.Messages))
	}
	for _, message := range filtered.Messages {
		if assistant, ok := message.(llm.AssistantMessage); ok {
			if assistant.StopReason == llm.StopReasonError || assistant.StopReason == llm.StopReasonAborted {
				t.Fatalf("failed assistant leaked into request context: %+v", assistant)
			}
		}
	}
}

// 工具批次执行中取消：未启动的工具以 aborted 错误结果收场，批次正常返回
func TestToolBatchAbortsPendingCalls(t *testing.T) {
	a := New(Options{})
	a.ctx.Tools = []llm.Tool{blockingTool{}, slowTool{}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	results, err := a.executeToolBatch(ctx, []llm.ToolCall{
		{ID: "c1", Name: "blocking", Arguments: map[string]any{}},
		{ID: "c2", Name: "slow", Arguments: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("batch should settle with results, got error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, result := range results {
		if !result.IsError {
			t.Fatalf("result for %s should be error after cancel: %+v", result.ToolCallID, result)
		}
		text, ok := result.Content[0].(llm.TextContent)
		if !ok || text.Text != "Operation aborted" {
			t.Fatalf("result text = %#v, want Operation aborted", result.Content)
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("batch took %v after cancel", elapsed)
	}
}

// 已完成的工具结果在取消后保留原内容，不被覆盖
func TestToolBatchKeepsCompletedResultsAfterCancel(t *testing.T) {
	a := New(Options{})
	a.ctx.Tools = []llm.Tool{instantTool{}, blockingTool{}}
	ctx, cancel := context.WithCancel(context.Background())
	// instant 立即返回，blocking 等待取消；50ms 后取消 → blocking 记为 aborted，instant 结果保留
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	results, err := a.executeToolBatch(ctx, []llm.ToolCall{
		{ID: "c1", Name: "instant", Arguments: map[string]any{}},
		{ID: "c2", Name: "blocking", Arguments: map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := results[0]
	if first.IsError {
		t.Fatalf("instant tool should keep its success result: %+v", first)
	}
	second := results[1]
	if !second.IsError {
		t.Fatalf("pending tool should be aborted: %+v", second)
	}
}

type blockingTool struct{}

func (blockingTool) Name() string               { return "blocking" }
func (blockingTool) Description() string        { return "blocks until cancelled" }
func (blockingTool) PromptSnippet() string      { return "" }
func (blockingTool) PromptGuidelines() []string { return nil }
func (blockingTool) Parameters() map[string]any {
	return map[string]any{}
}
func (blockingTool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	<-ctx.Done()
	return llm.ToolResult{}, ctx.Err()
}
func (blockingTool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

type slowTool struct{}

func (slowTool) Name() string               { return "slow" }
func (slowTool) Description() string        { return "slow tool" }
func (slowTool) PromptSnippet() string      { return "" }
func (slowTool) PromptGuidelines() []string { return nil }
func (slowTool) Parameters() map[string]any {
	return map[string]any{}
}
func (slowTool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	select {
	case <-ctx.Done():
		return llm.ToolResult{}, ctx.Err()
	case <-time.After(10 * time.Second):
		return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "slow done"}}}, nil
	}
}
func (slowTool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

type instantTool struct{}

func (instantTool) Name() string               { return "instant" }
func (instantTool) Description() string        { return "instant tool" }
func (instantTool) PromptSnippet() string      { return "" }
func (instantTool) PromptGuidelines() []string { return nil }
func (instantTool) Parameters() map[string]any {
	return map[string]any{}
}
func (instantTool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "instant done"}}}, nil
}
func (instantTool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

// assertAbortedEventSequence 校验收场的事件序列：MessageEnd + AgentEnd（与 error 收场一致，无 TurnEnd）
func assertAbortedEventSequence(t *testing.T, events []llm.AgentEvent) {
	t.Helper()
	var sawMessageEnd, sawAgentEnd bool
	for _, event := range events {
		switch event.(type) {
		case llm.MessageEndEvent:
			sawMessageEnd = true
		case llm.AgentEndEvent:
			sawAgentEnd = true
		}
	}
	if !sawMessageEnd || !sawAgentEnd {
		t.Fatalf("aborted run missing settlement events: message_end=%v agent_end=%v", sawMessageEnd, sawAgentEnd)
	}
}

// assertErrorEventSequence 校验 error 收场的事件序列
func assertErrorEventSequence(t *testing.T, events []llm.AgentEvent) {
	t.Helper()
	assertAbortedEventSequence(t, events)
}
