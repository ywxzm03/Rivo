package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// fakeTool 是测试用的工具，记录每次收到的参数
type fakeTool struct {
	name      string
	arguments []map[string]any
}

func (t *fakeTool) Name() string               { return t.name }
func (t *fakeTool) Description() string        { return "" }
func (t *fakeTool) Parameters() map[string]any { return nil }
func (t *fakeTool) ExecutionMode() llm.ToolExecutionMode {
	return llm.ToolExecutionParallel
}

func (t *fakeTool) Execute(_ context.Context, args map[string]any) (llm.ToolResult, error) {
	t.arguments = append(t.arguments, args)
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "pong"}}}, nil
}

var _ llm.Tool = (*fakeTool)(nil)

type controlledTool struct {
	name string
	mode llm.ToolExecutionMode
	run  func(context.Context) (llm.ToolResult, error)
}

func (t *controlledTool) Name() string               { return t.name }
func (t *controlledTool) Description() string        { return "" }
func (t *controlledTool) Parameters() map[string]any { return nil }
func (t *controlledTool) ExecutionMode() llm.ToolExecutionMode {
	return t.mode
}
func (t *controlledTool) Execute(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
	return t.run(ctx)
}

func waitForToolSignal(t *testing.T, signals <-chan string) string {
	t.Helper()
	select {
	case signal := <-signals:
		return signal
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tool execution")
		return ""
	}
}

func assertNoToolSignal(t *testing.T, signals <-chan string) {
	t.Helper()
	select {
	case signal := <-signals:
		t.Fatalf("tool %q started before the previous tool finished", signal)
	case <-time.After(50 * time.Millisecond):
	}
}

func toolCalls(names ...string) []llm.Content {
	calls := make([]llm.Content, len(names))
	for i, name := range names {
		calls[i] = llm.ToolCall{ID: name + "-call", Name: name, Arguments: map[string]any{}}
	}
	return calls
}

// doneStreamFn 造一个按调用次数依次返回预设回复的假 StreamFn
func doneStreamFn(calls *int, replies ...llm.AssistantMessage) llm.StreamFn {
	return func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.Event, error) {
		i := *calls
		*calls++
		ch := make(chan llm.Event, 1)
		reply := replies[min(i, len(replies)-1)]
		ch <- llm.Event{Type: llm.EventDone, Message: &reply}
		close(ch)
		return ch, nil
	}
}

func TestAgentRunToolLoop(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{
			Content:    []llm.Content{llm.ToolCall{ID: "c1", Name: "ping", Arguments: map[string]any{}}},
			StopReason: llm.StopReasonToolUse,
		},
		llm.AssistantMessage{
			Content:    []llm.Content{llm.TextContent{Text: "done"}},
			StopReason: llm.StopReasonStop,
		},
	)

	ping := &fakeTool{name: "ping"}
	a := NewAgent(
		llm.AgentConfig{Model: llm.Model{ID: "m1"}},
		llm.Context{Tools: []llm.Tool{ping}},
		streamFn,
		nil,
	)

	reply, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("StreamFn 调用 %d 次, want 2", calls)
	}
	if len(ping.arguments) != 1 {
		t.Errorf("工具被调用 %d 次, want 1（派发应走 AgentContext.Tools）", len(ping.arguments))
	}
	txt, ok := reply.Content[0].(llm.TextContent)
	if !ok || txt.Text != "done" {
		t.Errorf("reply.Content = %+v", reply.Content)
	}

	if got := len(a.AgentContext.Messages); got != 4 {
		t.Errorf("历史消息 %d 条, want 4", got)
	}
	if _, ok := a.AgentContext.Messages[0].(llm.UserMessage); !ok {
		t.Errorf("Messages[0] = %T, want UserMessage", a.AgentContext.Messages[0])
	}
}

func TestAgentRunsParallelToolsAndKeepsCallOrder(t *testing.T) {
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	var firstReleaseOnce, secondReleaseOnce sync.Once
	releaseFirst := func() { firstReleaseOnce.Do(func() { close(firstRelease) }) }
	releaseSecond := func() { secondReleaseOnce.Do(func() { close(secondRelease) }) }
	defer releaseFirst()
	defer releaseSecond()

	started := make(chan string, 2)
	finished := make(chan string, 2)
	makeTool := func(name string, release <-chan struct{}) *controlledTool {
		return &controlledTool{
			name: name,
			mode: llm.ToolExecutionParallel,
			run: func(context.Context) (llm.ToolResult, error) {
				started <- name
				<-release
				finished <- name
				return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: name}}}, nil
			},
		}
	}

	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: toolCalls("first", "second"), StopReason: llm.StopReasonToolUse},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}},
	)
	agent := NewAgent(llm.AgentConfig{}, llm.Context{Tools: []llm.Tool{
		makeTool("first", firstRelease),
		makeTool("second", secondRelease),
	}}, streamFn, nil)

	type runResult struct {
		err error
	}
	runDone := make(chan runResult, 1)
	go func() {
		_, err := agent.Run(context.Background(), "run")
		runDone <- runResult{err: err}
	}()

	starts := map[string]bool{waitForToolSignal(t, started): true}
	starts[waitForToolSignal(t, started)] = true
	if !starts["first"] || !starts["second"] {
		t.Fatalf("started tools = %v, want both parallel tools", starts)
	}

	releaseSecond()
	if got := waitForToolSignal(t, finished); got != "second" {
		t.Fatalf("first completed tool = %q, want second", got)
	}
	releaseFirst()
	if got := waitForToolSignal(t, finished); got != "first" {
		t.Fatalf("second completed tool = %q, want first", got)
	}
	select {
	case result := <-runDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run did not finish")
	}

	if got := calls; got != 2 {
		t.Fatalf("StreamFn called %d times, want 2", got)
	}
	firstResult, ok := agent.AgentContext.Messages[2].(llm.ToolResultMessage)
	if !ok {
		t.Fatalf("Messages[2] = %T, want ToolResultMessage", agent.AgentContext.Messages[2])
	}
	secondResult, ok := agent.AgentContext.Messages[3].(llm.ToolResultMessage)
	if !ok {
		t.Fatalf("Messages[3] = %T, want ToolResultMessage", agent.AgentContext.Messages[3])
	}
	if firstResult.ToolName != "first" || secondResult.ToolName != "second" {
		t.Fatalf("tool result order = %q, %q; want first, second", firstResult.ToolName, secondResult.ToolName)
	}
}

func TestAgentSequentialToolMakesBatchSequential(t *testing.T) {
	firstRelease := make(chan struct{})
	sequentialRelease := make(chan struct{})
	var firstOnce, sequentialOnce sync.Once
	releaseFirst := func() { firstOnce.Do(func() { close(firstRelease) }) }
	releaseSequential := func() { sequentialOnce.Do(func() { close(sequentialRelease) }) }
	defer releaseFirst()
	defer releaseSequential()

	started := make(chan string, 3)
	makeTool := func(name string, mode llm.ToolExecutionMode, release <-chan struct{}) *controlledTool {
		return &controlledTool{
			name: name,
			mode: mode,
			run: func(context.Context) (llm.ToolResult, error) {
				started <- name
				if release != nil {
					<-release
				}
				return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: name}}}, nil
			},
		}
	}

	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: toolCalls("first", "sequential", "last"), StopReason: llm.StopReasonToolUse},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}},
	)
	agent := NewAgent(llm.AgentConfig{}, llm.Context{Tools: []llm.Tool{
		makeTool("first", llm.ToolExecutionParallel, firstRelease),
		makeTool("sequential", llm.ToolExecutionSequential, sequentialRelease),
		makeTool("last", llm.ToolExecutionParallel, nil),
	}}, streamFn, nil)

	type runResult struct {
		err error
	}
	runDone := make(chan runResult, 1)
	go func() {
		_, err := agent.Run(context.Background(), "run")
		runDone <- runResult{err: err}
	}()

	if got := waitForToolSignal(t, started); got != "first" {
		t.Fatalf("first started tool = %q, want first", got)
	}
	assertNoToolSignal(t, started)
	releaseFirst()
	if got := waitForToolSignal(t, started); got != "sequential" {
		t.Fatalf("second started tool = %q, want sequential", got)
	}
	assertNoToolSignal(t, started)
	releaseSequential()
	if got := waitForToolSignal(t, started); got != "last" {
		t.Fatalf("third started tool = %q, want last", got)
	}
	select {
	case result := <-runDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run did not finish")
	}
}

func TestAgentToolErrorDoesNotPreventOtherParallelResults(t *testing.T) {
	sentinel := errors.New("tool failed")
	siblingRelease := make(chan struct{})
	var siblingReleaseOnce sync.Once
	releaseSibling := func() { siblingReleaseOnce.Do(func() { close(siblingRelease) }) }
	defer releaseSibling()
	started := make(chan string, 2)

	failing := &controlledTool{
		name: "failing",
		mode: llm.ToolExecutionParallel,
		run: func(context.Context) (llm.ToolResult, error) {
			started <- "failing"
			return llm.ToolResult{Details: map[string]any{"reason": "test"}}, sentinel
		},
	}
	sibling := &controlledTool{
		name: "sibling",
		mode: llm.ToolExecutionParallel,
		run: func(ctx context.Context) (llm.ToolResult, error) {
			started <- "sibling"
			select {
			case <-siblingRelease:
				return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "completed"}}}, nil
			case <-ctx.Done():
				return llm.ToolResult{}, ctx.Err()
			}
		},
	}

	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: toolCalls("failing", "sibling"), StopReason: llm.StopReasonToolUse},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}},
	)
	agent := NewAgent(llm.AgentConfig{}, llm.Context{Tools: []llm.Tool{failing, sibling}}, streamFn, nil)

	runDone := make(chan error, 1)
	go func() {
		_, err := agent.Run(context.Background(), "run")
		runDone <- err
	}()
	startedTools := map[string]bool{waitForToolSignal(t, started): true}
	startedTools[waitForToolSignal(t, started)] = true
	if !startedTools["failing"] || !startedTools["sibling"] {
		t.Fatalf("started tools = %v, want both parallel tools", startedTools)
	}
	releaseSibling()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run did not finish")
	}

	failureResult, ok := agent.AgentContext.Messages[2].(llm.ToolResultMessage)
	if !ok {
		t.Fatalf("Messages[2] = %T, want ToolResultMessage", agent.AgentContext.Messages[2])
	}
	siblingResult, ok := agent.AgentContext.Messages[3].(llm.ToolResultMessage)
	if !ok {
		t.Fatalf("Messages[3] = %T, want ToolResultMessage", agent.AgentContext.Messages[3])
	}
	if !failureResult.IsError || failureResult.ToolName != "failing" {
		t.Fatalf("failure result = %+v, want failing isError result", failureResult)
	}
	details, ok := failureResult.Details.(map[string]any)
	if !ok || details["reason"] != "test" {
		t.Fatalf("failure details = %#v, want reason=test", failureResult.Details)
	}
	failureText, _ := failureResult.Content[0].(llm.TextContent)
	if !strings.Contains(failureText.Text, sentinel.Error()) {
		t.Errorf("failure content = %q, want error text %q", failureText.Text, sentinel.Error())
	}
	if siblingResult.IsError || siblingResult.ToolName != "sibling" {
		t.Fatalf("sibling result = %+v, want successful sibling result", siblingResult)
	}
}

func TestAgentRunUnknownTool(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{
			Content:    []llm.Content{llm.ToolCall{ID: "c1", Name: "nope", Arguments: map[string]any{}}},
			StopReason: llm.StopReasonToolUse,
		},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}},
	)

	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, nil)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	if got := len(a.AgentContext.Messages); got != 4 {
		t.Fatalf("历史消息 %d 条, want 4", got)
	}
	tr, ok := a.AgentContext.Messages[2].(llm.ToolResultMessage)
	if !ok {
		t.Fatalf("Messages[2] = %T, want ToolResultMessage", a.AgentContext.Messages[2])
	}
	if !tr.IsError {
		t.Error("unknown tool result isError = false, want true")
	}
	text, _ := tr.Content[0].(llm.TextContent)
	if !strings.Contains(text.Text, "tool error:") {
		t.Errorf("工具结果 = %q, want 含 tool error:", text.Text)
	}
}

func TestAgentRunStreamError(t *testing.T) {
	sentinel := errors.New("boom")
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.Event, error) {
		ch := make(chan llm.Event, 1)
		ch <- llm.Event{Type: llm.EventError, Err: sentinel}
		close(ch)
		return ch, nil
	}
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, nil)
	if _, err := a.Run(context.Background(), "hi"); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}
