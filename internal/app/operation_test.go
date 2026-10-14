package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/session"
)

func operationApp(t *testing.T, stream llm.StreamFn, sink llm.AgentEventSink) *App {
	t.Helper()
	setupPromptEnv(t)
	cfg := configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel)
	spec := cfg.registry[deepseek.ProviderID]
	spec.new = func(string, string) ([]llm.Model, llm.StreamFn) { return spec.models, stream }
	cfg.registry[deepseek.ProviderID] = spec
	a, err := NewWithSessionDir(cfg, t.TempDir(), sink)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// blockSessionWrites 用目录阻塞文件打开，避免依赖当前进程的 chmod 权限
func blockSessionWrites(t *testing.T, a *App) func() {
	t.Helper()
	path := a.session.Path()
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".saved", path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOperationOwnershipCancellationAndJoin(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a := operationApp(t, func(ctx context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		events := make(chan llm.AssistantMessageEvent)
		go func() {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			close(events)
		}()
		return events, nil
	}, nil)
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := a.Start(context.Background(), OperationRequest{Kind: OperationThinking, Value: "off"}); err == nil {
		t.Fatal("accepted concurrent mutation")
	}
	op.Cancel()
	<-canceled
	select {
	case <-op.done:
		t.Fatal("operation completed before producer cleanup")
	default:
	}
	close(release)
	if result := op.Wait(); result.Err != nil || result.Reply.StopReason != llm.StopReasonAborted {
		t.Fatalf("cancel result = %+v", result)
	}
	if _, err := a.Start(context.Background(), OperationRequest{Kind: OperationThinking, Value: "off"}); err != nil {
		t.Fatal("completion retained operation ownership", err)
	}
	a.Wait()
}

func TestAssistantCommitRetryReusesOutputAndEntryID(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	a := operationApp(t, func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		close(started)
		<-release
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "generated once"}}}}
		close(events)
		return events, nil
	}, nil)
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	before := a.session.BuildContext()
	restore := blockSessionWrites(t, a)
	close(release)
	var commitErr *session.CommitError
	if result := op.Wait(); !errors.As(result.Err, &commitErr) {
		t.Fatalf("result = %+v, want storage failure", result)
	}
	if !reflect.DeepEqual(before, a.session.BuildContext()) || !reflect.DeepEqual(before.Messages(), a.agent.AgentContext.Messages) {
		t.Fatal("failed assistant commit changed authoritative context")
	}
	restore()
	reply, err := a.Run(context.Background(), "hello")
	if err != nil || reply == nil || calls != 1 {
		t.Fatalf("retry reply=%+v err=%v calls=%d", reply, err, calls)
	}
	reloaded, err := session.Open(a.session.Path(), "/project")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.BuildContext(), a.session.BuildContext()) {
		t.Fatal("disk and memory diverged after retry")
	}
}

func TestThinkingCommitFailureKeepsRuntimeSetting(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendMessage(textMsg("seed")); err != nil {
		t.Fatal(err)
	}
	before := a.CurrentThinkingLevel()
	restore := blockSessionWrites(t, a)
	if err := a.SetThinkingLevel(llm.ThinkingLevelHigh); err == nil {
		t.Fatal("want storage failure")
	}
	if a.CurrentThinkingLevel() != before {
		t.Fatal("runtime setting changed before persistence")
	}
	restore()
	if err := a.SetThinkingLevel(llm.ThinkingLevelHigh); err != nil {
		t.Fatal(err)
	}
	if a.CurrentThinkingLevel() != llm.ThinkingLevelHigh {
		t.Fatal("retried setting not published")
	}
}

type trackedTool struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (*trackedTool) Name() string                         { return "tracked" }
func (*trackedTool) Description() string                  { return "record execution" }
func (*trackedTool) PromptSnippet() string                { return "" }
func (*trackedTool) PromptGuidelines() []string           { return nil }
func (*trackedTool) Parameters() map[string]any           { return nil }
func (*trackedTool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }
func (tool *trackedTool) Execute(context.Context, map[string]any) (llm.ToolResult, error) {
	tool.calls.Add(1)
	tool.started <- struct{}{}
	<-tool.release
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "completed result"}}}, nil
}

func TestToolBatchCommitRetryDoesNotExecuteAgain(t *testing.T) {
	calls := 0
	a := operationApp(t, func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		message := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "finished"}}}
		if calls == 1 {
			message = &llm.AssistantMessage{Content: []llm.Content{
				llm.ToolCall{ID: "one", Name: "tracked"}, llm.ToolCall{ID: "two", Name: "tracked"},
			}, StopReason: llm.StopReasonToolUse}
		}
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: message}
		close(events)
		return events, nil
	}, nil)
	tool := &trackedTool{started: make(chan struct{}, 2), release: make(chan struct{})}
	a.agent.AgentContext.Tools = []llm.Tool{tool}
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "execute"})
	if err != nil {
		t.Fatal(err)
	}
	<-tool.started
	<-tool.started
	restore := blockSessionWrites(t, a)
	close(tool.release)
	if result := waitOperation(t, op); result.Err == nil {
		t.Fatal("want batch storage failure")
	}
	if calls != 1 || len(a.session.PendingMessages()) != 2 {
		t.Fatal("lost completed results or issued another model call")
	}
	restore()
	if _, err := a.Run(context.Background(), "execute"); err != nil {
		t.Fatal(err)
	}
	if tool.calls.Load() != 2 || calls != 2 {
		t.Fatalf("tool executions=%d model calls=%d", tool.calls.Load(), calls)
	}
	var results int
	for _, message := range a.session.BuildContext().Messages() {
		if _, ok := message.(llm.ToolResultMessage); ok {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("committed tool results=%d, want 2", results)
	}
}

func TestCompactionCommitFailureRetainsProjectionAndSummaryForRetry(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	a := operationApp(t, func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		close(started)
		<-release
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "summary"}}}}
		close(events)
		return events, nil
	}, nil)
	a.coordinator.Compactor.Config.KeepRecentTokens = 2
	for _, message := range []llm.Message{textMsg("earlier history"), textMsg("retained tail")} {
		if err := a.session.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	before := a.session.BuildContext()
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationCompact})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	restore := blockSessionWrites(t, a)
	close(release)
	if result := waitOperation(t, op); result.Err == nil {
		t.Fatal("want compaction storage failure")
	}
	if !reflect.DeepEqual(before, a.session.BuildContext()) {
		t.Fatal("failed compaction published projection")
	}
	restore()
	if err := a.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("retry regenerated summary")
	}
	if a.session.BuildContext().Entries[0].Summary != "summary" {
		t.Fatal("retry did not publish original summary")
	}
}

func TestPromptPatchCommitFailureDoesNotDisappearOnRetry(t *testing.T) {
	calls := 0
	a := operationApp(t, countingStreamFn(&calls, llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}), nil)
	if err := a.session.AppendMessage(llm.SystemMessage{Sections: prompt.Sections{"preamble": "old"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendMessage(textMsg("seed")); err != nil {
		t.Fatal(err)
	}
	a.coordinator.SetPromptSections(prompt.Sections{"preamble": "new"})
	restore := blockSessionWrites(t, a)
	if _, err := a.Run(context.Background(), "hello"); err == nil {
		t.Fatal("want patch storage failure")
	}
	if calls != 0 || llm.CurrentSystemMessage(a.agent.AgentContext.Messages).Sections["preamble"] != "old" {
		t.Fatal("failed patch changed request state")
	}
	restore()
	if _, err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || llm.CurrentSystemMessage(a.agent.AgentContext.Messages).Sections["preamble"] != "new" {
		t.Fatal("retried request lost system patch")
	}
}

func waitOperation(t *testing.T, op *Operation) OperationResult {
	t.Helper()
	select {
	case <-op.done:
		return op.Wait()
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not settle")
		return OperationResult{}
	}
}
