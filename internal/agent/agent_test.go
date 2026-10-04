package agent

import (
	"context"
	"errors"
	"fmt"
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
	return func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		i := *calls
		*calls++
		ch := make(chan llm.AssistantMessageEvent, 1)
		reply := replies[min(i, len(replies)-1)]
		ch <- llm.AssistantMessageDoneEvent{Message: &reply}
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
	toolEnds := make(chan string, 2)
	makeTool := func(name string, release <-chan struct{}) *controlledTool {
		return &controlledTool{
			name: name,
			mode: llm.ToolExecutionParallel,
			run: func(context.Context) (llm.ToolResult, error) {
				started <- name
				<-release
				return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: name}}}, nil
			},
		}
	}

	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: toolCalls("first", "second"), StopReason: llm.StopReasonToolUse},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}},
	)
	var events []llm.AgentEvent
	onEvent := func(event llm.AgentEvent) error {
		events = append(events, event)
		if end, ok := event.(llm.ToolExecutionEndEvent); ok {
			toolEnds <- end.ToolName
		}
		return nil
	}
	agent := NewAgent(llm.AgentConfig{}, llm.Context{Tools: []llm.Tool{
		makeTool("first", firstRelease),
		makeTool("second", secondRelease),
	}}, streamFn, onEvent)

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
	if got := waitForToolSignal(t, toolEnds); got != "second" {
		t.Fatalf("first tool_execution_end = %q, want second", got)
	}
	releaseFirst()
	if got := waitForToolSignal(t, toolEnds); got != "first" {
		t.Fatalf("second tool_execution_end = %q, want first", got)
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
	var startNames, endNames, resultStarts, resultEnds []string
	for _, event := range events {
		switch event := event.(type) {
		case llm.ToolExecutionStartEvent:
			startNames = append(startNames, event.ToolName)
		case llm.ToolExecutionEndEvent:
			endNames = append(endNames, event.ToolName)
		case llm.MessageStartEvent:
			if message, ok := event.Message.(llm.ToolResultMessage); ok {
				resultStarts = append(resultStarts, message.ToolName)
			}
		case llm.MessageEndEvent:
			if message, ok := event.Message.(llm.ToolResultMessage); ok {
				resultEnds = append(resultEnds, message.ToolName)
			}
		}
	}
	if strings.Join(startNames, ",") != "first,second" || strings.Join(endNames, ",") != "second,first" || strings.Join(resultStarts, ",") != "first,second" || strings.Join(resultEnds, ",") != "first,second" {
		t.Fatalf("tool event order = start:%v end:%v result-start:%v result-end:%v", startNames, endNames, resultStarts, resultEnds)
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

func TestAgentRunEmitsLifecycleEventsAndAssistantUpdates(t *testing.T) {
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent, 3)
		ch <- llm.AssistantMessageTextDeltaEvent{Delta: "hel"}
		ch <- llm.AssistantMessageTextDeltaEvent{Delta: "lo"}
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
			Content:    []llm.Content{llm.TextContent{Text: "hello"}},
			StopReason: llm.StopReasonStop,
		}}
		close(ch)
		return ch, nil
	}
	var events []llm.AgentEvent
	a := NewAgent(llm.AgentConfig{Model: llm.Model{
		ID:       "model-id",
		Provider: "provider-id",
		API:      llm.APIOpenAIResponses,
	}}, llm.Context{}, streamFn, func(event llm.AgentEvent) error {
		events = append(events, event)
		return nil
	})

	message, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{
		llm.AgentEventAgentStart,
		llm.AgentEventTurnStart,
		llm.AgentEventMessageStart,
		llm.AgentEventMessageEnd,
		llm.AgentEventMessageStart,
		llm.AgentEventMessageUpdate,
		llm.AgentEventMessageUpdate,
		llm.AgentEventMessageEnd,
		llm.AgentEventTurnEnd,
		llm.AgentEventAgentEnd,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Type() != want {
			t.Errorf("events[%d].Type() = %q, want %q", i, events[i].Type(), want)
		}
	}
	firstUpdateEvent, ok := events[5].(llm.MessageUpdateEvent)
	if !ok {
		t.Fatalf("events[5] = %T, want MessageUpdateEvent", events[5])
	}
	firstUpdate, ok := firstUpdateEvent.Message.(llm.AssistantMessage)
	if !ok || firstUpdate.Content[0].(llm.TextContent).Text != "hel" {
		t.Fatalf("first shared partial message = %#v", firstUpdateEvent.Message)
	}
	secondUpdateEvent, ok := events[6].(llm.MessageUpdateEvent)
	if !ok {
		t.Fatalf("events[6] = %T, want MessageUpdateEvent", events[6])
	}
	secondUpdate, ok := secondUpdateEvent.Message.(llm.AssistantMessage)
	if !ok || secondUpdate.Content[0].(llm.TextContent).Text != "hello" {
		t.Fatalf("second partial message = %#v", secondUpdateEvent.Message)
	}
	firstDelta := firstUpdateEvent.AssistantMessageEvent.(llm.AssistantMessageTextDeltaEvent)
	secondDelta := secondUpdateEvent.AssistantMessageEvent.(llm.AssistantMessageTextDeltaEvent)
	if firstDelta.Delta != "hel" || secondDelta.Delta != "lo" {
		t.Fatalf("assistant stream events = %+v, %+v", firstUpdateEvent.AssistantMessageEvent, secondUpdateEvent.AssistantMessageEvent)
	}
	if message.API != llm.APIOpenAIResponses || message.Provider != "provider-id" || message.Model != "model-id" {
		t.Errorf("assistant metadata = api:%q provider:%q model:%q", message.API, message.Provider, message.Model)
	}
	agentEnd, ok := events[9].(llm.AgentEndEvent)
	if !ok || len(agentEnd.Messages) != 2 {
		t.Errorf("agent_end event = %#v, want two messages", events[9])
	}
}

func TestAgentAccumulatesIndexedEventsWithoutPartialSnapshots(t *testing.T) {
	toolCall := llm.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"path": "a.txt"}}
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent, 10)
		ch <- llm.AssistantMessageTextStartEvent{ContentIndex: 0}
		ch <- llm.AssistantMessageTextDeltaEvent{ContentIndex: 0, Delta: "answer"}
		ch <- llm.AssistantMessageTextEndEvent{ContentIndex: 0, Content: "answer"}
		ch <- llm.AssistantMessageThinkingStartEvent{ContentIndex: 1}
		ch <- llm.AssistantMessageThinkingDeltaEvent{ContentIndex: 1, Delta: "plan"}
		ch <- llm.AssistantMessageThinkingEndEvent{ContentIndex: 1, Content: "plan"}
		ch <- llm.AssistantMessageToolCallStartEvent{ContentIndex: 2, ToolCall: toolCall}
		ch <- llm.AssistantMessageToolCallDeltaEvent{ContentIndex: 2, Delta: `{"path":"a.txt"}`}
		ch <- llm.AssistantMessageToolCallEndEvent{ContentIndex: 2, ToolCall: toolCall}
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "answer"}}}}
		close(ch)
		return ch, nil
	}
	var updates []llm.AssistantMessage
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, func(event llm.AgentEvent) error {
		update, ok := event.(llm.MessageUpdateEvent)
		if !ok {
			return nil
		}
		message, ok := update.Message.(llm.AssistantMessage)
		if !ok {
			t.Fatalf("message update has message type %T", update.Message)
		}
		updates = append(updates, message)
		return nil
	})

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 9 {
		t.Fatalf("message update count = %d, want 9", len(updates))
	}
	got := updates[len(updates)-1].Content
	if len(got) != 3 {
		t.Fatalf("partial content length = %d, want 3: %#v", len(got), got)
	}
	if text, ok := got[0].(llm.TextContent); !ok || text.Text != "answer" {
		t.Errorf("text content = %#v, want answer", got[0])
	}
	if thinking, ok := got[1].(llm.ThinkingContent); !ok || thinking.Thinking != "plan" {
		t.Errorf("thinking content = %#v, want plan", got[1])
	}
	if call, ok := got[2].(llm.ToolCall); !ok || call.ID != toolCall.ID || call.Name != toolCall.Name || call.Arguments["path"] != "a.txt" {
		t.Errorf("tool call = %#v, want %#v", got[2], toolCall)
	}
}

func TestAgentRunStreamError(t *testing.T) {
	sentinel := errors.New("boom")
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageErrorEvent{Err: sentinel}
		close(ch)
		return ch, nil
	}
	var events []llm.AgentEvent
	a := NewAgent(llm.AgentConfig{Model: llm.Model{ID: "m1"}}, llm.Context{}, streamFn, func(event llm.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	message, err := a.Run(context.Background(), "hi")
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
	if message == nil || message.StopReason != llm.StopReasonError || message.Model != "m1" {
		t.Fatalf("error assistant message = %+v", message)
	}
	if len(message.Content) != 1 {
		t.Fatalf("error assistant content = %+v, want one error text", message.Content)
	}
	errorText, ok := message.Content[0].(llm.TextContent)
	if !ok || errorText.Text != sentinel.Error() {
		t.Errorf("error assistant content = %+v, want original error", message.Content)
	}
	if got := len(a.AgentContext.Messages); got != 2 {
		t.Fatalf("context messages = %d, want user and error assistant", got)
	}
	if len(events) < 2 || events[len(events)-2].Type() != llm.AgentEventMessageEnd || events[len(events)-1].Type() != llm.AgentEventAgentEnd {
		t.Errorf("last events = %+v, want message_end then agent_end", events)
	}
}

func TestAgentEventSinkErrorStopsRunAndCancelsModelStream(t *testing.T) {
	sentinel := errors.New("sink failed")
	calls := 0
	canceled := make(chan struct{})
	streamFn := func(ctx context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		ch := make(chan llm.AssistantMessageEvent)
		go func() {
			defer close(ch)
			select {
			case ch <- llm.AssistantMessageTextDeltaEvent{Delta: "partial"}:
				<-ctx.Done()
			case <-ctx.Done():
			}
			close(canceled)
		}()
		return ch, nil
	}
	var events []llm.AgentEvent
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, func(event llm.AgentEvent) error {
		events = append(events, event)
		if event.Type() == llm.AgentEventMessageUpdate {
			return sentinel
		}
		return nil
	})
	if _, err := a.Run(context.Background(), "hi"); !errors.Is(err, sentinel) {
		t.Fatalf("Run() error = %v, want sink error", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("model stream did not observe cancellation")
	}
	if calls != 1 {
		t.Errorf("StreamFn calls = %d, want 1", calls)
	}
	if events[len(events)-1].Type() != llm.AgentEventMessageUpdate {
		t.Errorf("last event = %q, want message_update", events[len(events)-1].Type())
	}
}

func TestAgentToolEventSinkErrorCancelsOtherParallelTools(t *testing.T) {
	sentinel := errors.New("tool event sink failed")
	canceled := make(chan struct{})
	fast := &controlledTool{
		name: "fast",
		mode: llm.ToolExecutionParallel,
		run: func(context.Context) (llm.ToolResult, error) {
			return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "done"}}}, nil
		},
	}
	slow := &controlledTool{
		name: "slow",
		mode: llm.ToolExecutionParallel,
		run: func(ctx context.Context) (llm.ToolResult, error) {
			<-ctx.Done()
			close(canceled)
			return llm.ToolResult{}, ctx.Err()
		},
	}
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: toolCalls("fast", "slow"), StopReason: llm.StopReasonToolUse},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "must not run"}}},
	)
	var events []llm.AgentEvent
	a := NewAgent(llm.AgentConfig{}, llm.Context{Tools: []llm.Tool{fast, slow}}, streamFn, func(event llm.AgentEvent) error {
		events = append(events, event)
		if end, ok := event.(llm.ToolExecutionEndEvent); ok && end.ToolName == "fast" {
			return sentinel
		}
		return nil
	})
	if _, err := a.Run(context.Background(), "hi"); !errors.Is(err, sentinel) {
		t.Fatalf("Run() error = %v, want sink error", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("unfinished parallel tool did not receive cancellation")
	}
	if calls != 1 {
		t.Errorf("StreamFn calls = %d, want 1", calls)
	}
	for _, event := range events {
		switch event := event.(type) {
		case llm.MessageStartEvent:
			if _, ok := event.Message.(llm.ToolResultMessage); ok {
				t.Error("tool result message emitted after event sink failed")
			}
		case llm.MessageEndEvent:
			if _, ok := event.Message.(llm.ToolResultMessage); ok {
				t.Error("tool result message emitted after event sink failed")
			}
		}
	}
}

// blockedStreamFn 造一个在 StreamFn 中阻塞直到收到释放信号的假流
func blockedStreamFn(started chan struct{}, release chan struct{}) llm.StreamFn {
	return func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		started <- struct{}{}
		<-release
		ch := make(chan llm.AssistantMessageEvent, 1)
		reply := llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}}
		ch <- llm.AssistantMessageDoneEvent{Message: &reply}
		close(ch)
		return ch, nil
	}
}

func TestAgentRunSerializesConcurrentRuns(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, blockedStreamFn(started, release), nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "first")
		firstDone <- err
	}()
	<-started

	secondDone := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "second")
		secondDone <- err
	}()
	// 第二次运行必须等第一次完成后才能进入模型流
	select {
	case <-started:
		t.Fatal("second Run started while the first Run was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	// 两次运行的消息在会话中按序成对，不互相混入
	if len(a.AgentContext.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(a.AgentContext.Messages))
	}
	for i, want := range []string{"first", "ok", "second", "ok"} {
		message := a.AgentContext.Messages[i]
		if i%2 == 1 {
			assistant, ok := message.(llm.AssistantMessage)
			if !ok || len(assistant.Content) != 1 {
				t.Fatalf("message %d = %#v, want assistant message", i, message)
			}
			if text, ok := assistant.Content[0].(llm.TextContent); !ok || text.Text != want {
				t.Fatalf("message %d = %#v, want text %q", i, message, want)
			}
			continue
		}
		user, ok := message.(llm.UserMessage)
		if !ok || len(user.Content) != 1 {
			t.Fatalf("message %d = %#v, want user message", i, message)
		}
		if text, ok := user.Content[0].(llm.TextContent); !ok || text.Text != want {
			t.Fatalf("message %d = %#v, want text %q", i, message, want)
		}
	}
}

func TestAgentRunCanceledWhileQueued(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, blockedStreamFn(started, release), nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "first")
		firstDone <- err
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, "second")
		secondDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	// 排队等待期间被取消的运行不得修改会话
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("second Run() error = %v, want context.Canceled", err)
	}
	if len(a.AgentContext.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(a.AgentContext.Messages))
	}
	user, ok := a.AgentContext.Messages[0].(llm.UserMessage)
	if !ok || len(user.Content) != 1 {
		t.Fatalf("message 0 = %#v, want user message", a.AgentContext.Messages[0])
	}
	if text, ok := user.Content[0].(llm.TextContent); !ok || text.Text != "first" {
		t.Fatalf("message 0 = %#v, want text %q", a.AgentContext.Messages[0], "first")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestAgentRunRejectsCanceledContext(t *testing.T) {
	calls := 0
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{}}
		close(ch)
		return ch, nil
	}
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, nil)

	// 守卫空闲且 ctx 已取消时 select 两个分支同时就绪，Run 必须放弃执行而不是随机抢占守卫
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Run(ctx, "canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("StreamFn calls = %d, want 0", calls)
	}
	if len(a.AgentContext.Messages) != 0 {
		t.Fatalf("messages = %d, want 0", len(a.AgentContext.Messages))
	}
}

// recordingCoordinator 记录回调调用的假编排层
type recordingCoordinator struct {
	compactions int
	notifies    int
	// replace 内部执行内存替换，模拟编排层压缩后的投影提交
	replace    func()
	compactErr error
}

func (c *recordingCoordinator) CompactIfNeeded(_ context.Context) (bool, error) {
	c.compactions++
	if c.compactErr != nil {
		return false, c.compactErr
	}
	if c.replace != nil {
		// 触发的测试用例通过闭包替换 Agent 内存
		replace := c.replace
		c.replace = nil
		replace()
		return true, nil
	}
	return false, nil
}

func (c *recordingCoordinator) NotifyOverflow() { c.notifies++ }

// TestAgentContinueContinuesWithoutUserMessage 验证续接不追加用户消息，事件序列与运行同构
func TestAgentContinueContinuesWithoutUserMessage(t *testing.T) {
	var events []llm.AgentEvent
	calls := 0
	streamFn := doneStreamFn(&calls, llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "resumed"}}, StopReason: llm.StopReasonStop})
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, func(event llm.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	a.AgentContext.Messages = append(a.AgentContext.Messages,
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "earlier"}}},
	)

	reply, err := a.Continue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("stream calls = %d, want 1", calls)
	}
	// 不追加用户消息：只有预置历史 + 本轮 assistant 回复
	if got := len(a.AgentContext.Messages); got != 2 {
		t.Fatalf("messages = %d, want 2", got)
	}
	if _, ok := a.AgentContext.Messages[0].(llm.UserMessage); !ok {
		t.Errorf("messages[0] = %T, want seeded user message", a.AgentContext.Messages[0])
	}
	wantTypes := []string{
		llm.AgentEventAgentStart,
		llm.AgentEventTurnStart,
		llm.AgentEventMessageStart,
		llm.AgentEventMessageEnd,
		llm.AgentEventTurnEnd,
		llm.AgentEventAgentEnd,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %d, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Type() != want {
			t.Errorf("events[%d] = %q, want %q", i, events[i].Type(), want)
		}
	}
	if text := reply.Content[0].(llm.TextContent).Text; text != "resumed" {
		t.Errorf("reply = %q, want resumed", text)
	}
}

// TestAgentCompactIfNeededReplacesMemory 验证循环顶压缩回调替换内存后，agent_end 汇总不越界
func TestAgentCompactIfNeededReplacesMemory(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: toolCalls("ping"), StopReason: llm.StopReasonToolUse},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}, StopReason: llm.StopReasonStop},
	)
	var events []llm.AgentEvent
	coord := &recordingCoordinator{}
	a := NewAgent(llm.AgentConfig{}, llm.Context{Tools: []llm.Tool{&fakeTool{name: "ping"}}}, streamFn, func(event llm.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	coord.replace = func() {
		// 模拟编排层压缩：内存替换为摘要 + 保留尾部，只在首轮压缩一次
		summary := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "wrapped summary"}}}
		a.AgentContext.Messages = append([]llm.Message{summary}, a.AgentContext.Messages...)
		coord.replace = nil
	}
	a.Coordinator = coord

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if coord.compactions < 1 {
		t.Fatalf("compactions = %d, want >= 1", coord.compactions)
	}
	// 压缩后 agent_end 只汇总压缩后的新增消息，不越界
	var agentEnd *llm.AgentEndEvent
	for i := range events {
		if e, ok := events[i].(llm.AgentEndEvent); ok {
			agentEnd = &e
		}
	}
	if agentEnd == nil {
		t.Fatal("agent_end event missing")
	}
	// agent_end 汇总压缩后的新增消息：工具调用 assistant、工具结果、最终回复
	if len(agentEnd.Messages) != 3 {
		t.Errorf("agent_end messages = %d, want 3", len(agentEnd.Messages))
	}
}

// TestAgentNotifiesOverflowOnError 验证显式溢出错误经 finishModelError 收场前通知编排层
func TestAgentNotifiesOverflowOnError(t *testing.T) {
	calls := 0
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageErrorEvent{Err: fmt.Errorf("openai: 400: %w", llm.ErrContextOverflow)}
		close(ch)
		return ch, nil
	}
	coord := &recordingCoordinator{}
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, nil)
	a.Coordinator = coord

	message, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("overflow error should end the run")
	}
	if message == nil || message.StopReason != llm.StopReasonError {
		t.Fatalf("message = %+v, want error assistant", message)
	}
	if coord.notifies != 1 {
		t.Errorf("notifies = %d, want 1", coord.notifies)
	}
	// 失败尝试照常提交进上下文
	if got := len(a.AgentContext.Messages); got != 2 {
		t.Errorf("messages = %d, want 2", got)
	}
}

// TestAgentNotifiesOverflowOnRecoverableLength 验证可恢复的长度截断提交消息后通知编排层，且不执行工具
func TestAgentNotifiesOverflowOnRecoverableLength(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: toolCalls("ping"), StopReason: llm.StopReasonLength, Usage: llm.Usage{OutputTokens: 50}},
	)
	coord := &recordingCoordinator{}
	a := NewAgent(llm.AgentConfig{Model: llm.Model{MaxTokens: 100}}, llm.Context{Tools: []llm.Tool{&fakeTool{name: "ping"}}}, streamFn, nil)
	a.Coordinator = coord

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if coord.notifies != 1 {
		t.Errorf("notifies = %d, want 1", coord.notifies)
	}
	// 截断消息正常提交进上下文
	if got := len(a.AgentContext.Messages); got != 2 {
		t.Errorf("messages = %d, want 2", got)
	}
}

// TestAgentLengthAtMaxTokensNotRecovered 验证达到输出上限的截断不通知编排层
func TestAgentLengthAtMaxTokensNotRecovered(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "capped"}}, StopReason: llm.StopReasonLength, Usage: llm.Usage{OutputTokens: 100}},
	)
	coord := &recordingCoordinator{}
	a := NewAgent(llm.AgentConfig{Model: llm.Model{MaxTokens: 100}}, llm.Context{}, streamFn, nil)
	a.Coordinator = coord

	reply, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if coord.notifies != 0 {
		t.Errorf("notifies = %d, want 0", coord.notifies)
	}
	if reply.StopReason != llm.StopReasonLength {
		t.Errorf("reply stop reason = %q, want length", reply.StopReason)
	}
}

// TestAgentCompactionDeadlineEndsRun 验证压缩回调超时中止直接结束运行
func TestAgentCompactionDeadlineEndsRun(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls, llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}})
	coord := &recordingCoordinator{compactErr: context.DeadlineExceeded}
	a := NewAgent(llm.AgentConfig{}, llm.Context{}, streamFn, nil)
	a.Coordinator = coord

	if _, err := a.Run(context.Background(), "hi"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context.DeadlineExceeded", err)
	}
	if calls != 0 {
		t.Errorf("stream calls = %d, want 0 (expired ctx must not reach the model)", calls)
	}
}

// TestAgentRecoverableLengthWithoutCoordinatorRunsTools 验证未启用编排层时，
// 可恢复的长度截断不跳过工具执行（与旧行为一致）
func TestAgentRecoverableLengthWithoutCoordinatorRunsTools(t *testing.T) {
	calls := 0
	streamFn := doneStreamFn(&calls,
		llm.AssistantMessage{Content: toolCalls("ping"), StopReason: llm.StopReasonLength, Usage: llm.Usage{OutputTokens: 50}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}, StopReason: llm.StopReasonStop},
	)
	ping := &fakeTool{name: "ping"}
	a := NewAgent(llm.AgentConfig{Model: llm.Model{MaxTokens: 100}}, llm.Context{Tools: []llm.Tool{ping}}, streamFn, nil)

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(ping.arguments) != 1 {
		t.Errorf("tool calls = %d, want 1 (tools must run without a coordinator)", len(ping.arguments))
	}
	if calls != 2 {
		t.Errorf("stream calls = %d, want 2", calls)
	}
	if got := len(a.AgentContext.Messages); got != 4 {
		t.Errorf("messages = %d, want 4", got)
	}
}
