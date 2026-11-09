package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/session"
)

func operationApp(t *testing.T, stream llm.StreamFn, sink llm.AgentEventSink) *App {
	t.Helper()
	setupPromptEnv(t)
	cfg := configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel)
	cfg = cfg.WithStreamForTest(deepseek.ProviderID, cfg.AvailableModels(), stream)
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
	for _, kind := range []OperationKind{OperationThinking, OperationClear, OperationDelete} {
		if _, err := a.Start(context.Background(), OperationRequest{Kind: kind, Value: "off"}); err == nil {
			t.Fatalf("accepted concurrent %s operation", kind)
		}
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

func TestAppSteeringRunsWithinTheActiveOperation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	a := operationApp(t, func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
		}
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "reply"}}}}
		close(events)
		return events, nil
	}, nil)
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := a.EnqueueSteering("steer"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if result := op.Wait(); result.Err != nil {
		t.Fatal(result.Err)
	}
	if calls != 2 {
		t.Fatalf("model calls = %d, want initial response plus steering response", calls)
	}
	var users []string
	for _, message := range a.ContextView().Messages() {
		if user, ok := message.(llm.UserMessage); ok {
			users = append(users, messageTextForTest(t, user))
		}
	}
	if len(users) != 2 || users[0] != "initial" || users[1] != "steer" {
		t.Fatalf("user messages = %v", users)
	}
}

type steeringWithdrawRunner struct {
	withdraw func()
	calls    int
}

func (r *steeringWithdrawRunner) Run(context.Context, string) (*llm.AssistantMessage, error) {
	return nil, nil
}

func (r *steeringWithdrawRunner) Continue(context.Context) (*llm.AssistantMessage, error) {
	return nil, nil
}

func (r *steeringWithdrawRunner) Steer(string) error { return nil }

func (r *steeringWithdrawRunner) SteerMessage(llm.UserMessage) error { return nil }

func (r *steeringWithdrawRunner) FollowUpMessage(llm.UserMessage) error { return nil }

func (r *steeringWithdrawRunner) ContinueSteering(context.Context) (*llm.AssistantMessage, error) {
	r.calls++
	r.withdraw()
	return nil, nil
}

func TestContinueQueuedPreservesReplyWhenSteeringIsWithdrawn(t *testing.T) {
	a := operationApp(t, nil, nil)
	runner := &steeringWithdrawRunner{}
	runner.withdraw = func() { a.agent.Take(agent.QueueSteering) }
	a.coordinator.Agent = runner
	a.active = &Operation{kind: OperationRun}
	if err := a.agent.Steer("withdraw me"); err != nil {
		t.Fatal(err)
	}
	want := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "reply"}}}
	result := a.continueQueuedSteering(context.Background(), OperationResult{Reply: want})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Reply != want {
		t.Fatalf("reply = %p, want original %p", result.Reply, want)
	}
	if runner.calls != 1 {
		t.Fatalf("ContinueSteering calls = %d, want one", runner.calls)
	}
}

func TestContinueQueuedDrainsSteeringBeforeFollowUp(t *testing.T) {
	a := operationApp(t, nil, nil)
	runner := &orderRecordingRunner{queue: a.agent}
	a.coordinator.Agent = runner
	a.active = &Operation{kind: OperationRun}
	if err := a.agent.Steer("steer me"); err != nil {
		t.Fatal(err)
	}
	if err := a.agent.FollowUp("follow me"); err != nil {
		t.Fatal(err)
	}
	// 后继 run 期间再入队一条 steering，收尾循环必须重新消费它
	runner.onContinueFollowUp = func() {
		if err := a.agent.Steer("late steering"); err != nil {
			t.Fatal(err)
		}
	}
	result := a.continueQueuedSteering(context.Background(), OperationResult{})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	want := []string{"steering", "follow-up", "steering"}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("continuation order = %v, want %v", runner.calls, want)
	}
	if a.agent.Len(agent.QueueSteering) > 0 || a.agent.Len(agent.QueueFollowUp) > 0 {
		t.Fatalf("queues not drained: %v / %v", a.agent.Messages(agent.QueueSteering), a.agent.Messages(agent.QueueFollowUp))
	}
	if !a.active.closing {
		t.Fatal("operation stayed open after queues drained")
	}
}

// orderRecordingRunner 记录 continuation 调用顺序，并可注入后继期间的入队
type orderRecordingRunner struct {
	queue              *agent.Agent
	calls              []string
	onContinueFollowUp func()
}

func (r *orderRecordingRunner) Run(context.Context, string) (*llm.AssistantMessage, error) {
	return nil, nil
}

func (r *orderRecordingRunner) Continue(context.Context) (*llm.AssistantMessage, error) {
	return nil, nil
}

func (r *orderRecordingRunner) Steer(string) error { return nil }

func (r *orderRecordingRunner) SteerMessage(llm.UserMessage) error { return nil }

func (r *orderRecordingRunner) FollowUpMessage(llm.UserMessage) error { return nil }

func (r *orderRecordingRunner) ContinueSteering(context.Context) (*llm.AssistantMessage, error) {
	r.calls = append(r.calls, "steering")
	r.queue.Take(agent.QueueSteering)
	return nil, nil
}

func (r *orderRecordingRunner) ContinueFollowUp(context.Context) (*llm.AssistantMessage, error) {
	r.calls = append(r.calls, "follow-up")
	r.queue.Take(agent.QueueFollowUp)
	if r.onContinueFollowUp != nil {
		r.onContinueFollowUp()
		r.onContinueFollowUp = nil
	}
	return nil, nil
}

func TestEnqueueFollowUpRejectedDuringManualCompact(t *testing.T) {
	a := operationApp(t, nil, nil)
	a.active = &Operation{kind: OperationCompact}
	if err := a.EnqueueFollowUp("during compact"); !errors.Is(err, ErrFollowUpUnavailable) {
		t.Fatalf("EnqueueFollowUp() error = %v, want unavailable", err)
	}
	a.active = &Operation{kind: OperationRun}
	if err := a.EnqueueFollowUp("during run"); err != nil {
		t.Fatalf("EnqueueFollowUp() during run error = %v", err)
	}
}

// 收尾窗口关闭后，新入队必须被拒绝而不是静默留在队列里
func TestContinueQueuedClosesSubmissionWindowAtomically(t *testing.T) {
	a := operationApp(t, nil, nil)
	runner := &orderRecordingRunner{queue: a.agent}
	a.coordinator.Agent = runner
	a.active = &Operation{kind: OperationRun}
	result := a.continueQueuedSteering(context.Background(), OperationResult{})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !a.active.closing {
		t.Fatal("submission window stayed open with empty queues")
	}
	if err := a.EnqueueSteering("after teardown"); !errors.Is(err, ErrSteeringUnavailable) {
		t.Fatalf("EnqueueSteering() after teardown = %v, want unavailable", err)
	}
	if err := a.EnqueueFollowUp("after teardown"); !errors.Is(err, ErrFollowUpUnavailable) {
		t.Fatalf("EnqueueFollowUp() after teardown = %v, want unavailable", err)
	}
	if a.agent.Len(agent.QueueSteering) > 0 || a.agent.Len(agent.QueueFollowUp) > 0 {
		t.Fatalf("rejected input leaked into queues: %v / %v", a.agent.Messages(agent.QueueSteering), a.agent.Messages(agent.QueueFollowUp))
	}
}

func TestClearOperationMovesLeafWithoutChangingFileOrSettings(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.SetThinkingLevel(llm.ThinkingLevelHigh); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendMessage(textMsg("old conversation")); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendSessionInfo("Current thread"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.session.Path())
	if err != nil {
		t.Fatal(err)
	}
	model := a.CurrentModel()
	thinking := a.CurrentThinkingLevel()

	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationClear})
	if err != nil {
		t.Fatal(err)
	}
	if result := op.Wait(); result.Err != nil || result.Exit {
		t.Fatalf("clear result = %+v", result)
	}
	if a.session.LeafID() != "" || len(a.ContextView().Messages()) != 0 || len(a.agent.Context().Messages) != 0 {
		t.Fatal("clear retained the active context")
	}
	if a.CurrentModel().Provider != model.Provider || a.CurrentModel().ID != model.ID || a.CurrentThinkingLevel() != thinking {
		t.Fatal("clear changed runtime settings")
	}
	if a.SessionName() != "Current thread" {
		t.Fatal("clear changed session name")
	}
	after, err := os.ReadFile(a.session.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("clear changed the session file")
	}
	if len(a.Tree().Entries) == 0 {
		t.Fatal("clear discarded the session tree")
	}

	if err := a.session.AppendMessage(textMsg("new root")); err != nil {
		t.Fatal(err)
	}
	entries := a.Tree().Entries
	if entries[len(entries)-1].ParentID != "" {
		t.Fatalf("new message parent = %q, want empty root", entries[len(entries)-1].ParentID)
	}
}

func TestDeleteOperationRemovesFilesAndRequestsExit(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendMessage(textMsg("delete me")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.session.Path()+".draft", []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationDelete})
	if err != nil {
		t.Fatal(err)
	}
	result := op.Wait()
	if result.Err != nil || !result.Exit {
		t.Fatalf("delete result = %+v", result)
	}
	for _, path := range []string{a.session.Path(), a.session.Path() + ".draft"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists, err = %v", path, err)
		}
	}
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
	if !reflect.DeepEqual(before, a.session.BuildContext()) || !reflect.DeepEqual(before.Messages(), a.agent.Context().Messages) {
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

func TestSteeringCommitRetryDoesNotDuplicateQueuedMessage(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	turnEnded, allowSteering := make(chan struct{}), make(chan struct{})
	var firstTurn sync.Once
	calls := 0
	stream := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
		}
		reply := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "reply"}}}
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: reply}
		close(events)
		return events, nil
	}
	sink := func(event llm.AgentEvent) error {
		if _, ok := event.(llm.TurnEndEvent); ok {
			firstTurn.Do(func() {
				close(turnEnded)
				<-allowSteering
			})
		}
		return nil
	}
	a := operationApp(t, stream, sink)
	if err := a.session.AppendMessage(textMsg("seed")); err != nil {
		t.Fatal(err)
	}
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := a.EnqueueSteering("queued"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-turnEnded
	restore := blockSessionWrites(t, a)
	close(allowSteering)
	if result := op.Wait(); result.Err == nil {
		t.Fatal("want steering commit failure")
	}
	if got := a.QueueMessages(agent.QueueSteering); len(got) != 1 || got[0] != "queued" {
		t.Fatalf("steering queue = %v, want queued message restored", got)
	}
	restore()
	if _, err := a.Run(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if got := a.QueueMessages(agent.QueueSteering); len(got) != 0 {
		t.Fatalf("steering queue after retry = %v, want empty", got)
	}
	queued, next := 0, 0
	for _, message := range a.ContextView().Messages() {
		user, ok := message.(llm.UserMessage)
		if !ok || len(user.Content) != 1 {
			continue
		}
		text, _ := user.Content[0].(llm.TextContent)
		switch text.Text {
		case "queued":
			queued++
		case "next":
			next++
		}
	}
	if queued != 1 || next != 1 {
		t.Fatalf("retried user messages = queued:%d next:%d, want one each", queued, next)
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

func TestRetryPendingClearsRequestBeforeSettingsRestore(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendMessage(textMsg("seed")); err != nil {
		t.Fatal(err)
	}
	restore := blockSessionWrites(t, a)
	if err := a.session.AppendModelChange(deepseek.ProviderID, "missing-model"); err == nil {
		t.Fatal("want pending model change")
	}
	restore()
	a.pendingRequest = &OperationRequest{Kind: OperationModel, Value: deepseek.ProviderID + "/missing-model"}

	result := a.executeOperation(context.Background(), OperationRequest{Kind: OperationModel, Value: deepseek.ProviderID + "/missing-model"})
	if result.Err == nil {
		t.Fatal("want settings restore failure")
	}
	if a.pendingRequest != nil {
		t.Fatalf("pendingRequest = %+v, want nil after successful pending retry", a.pendingRequest)
	}
}

func TestImagePendingIsRejectedBeforeReplayOnTextOnlyModel(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendMessage(textMsg("seed")); err != nil {
		t.Fatal(err)
	}
	restore := blockSessionWrites(t, a)
	err := a.session.AppendMessage(llm.UserMessage{Content: []llm.Content{
		llm.TextContent{Text: "describe"},
		llm.ImageContent{Data: "aGk=", MIMEType: "image/png"},
	}})
	if err == nil {
		restore()
		t.Fatal("want pending image commit failure")
	}
	restore()
	if !a.session.HasPending() {
		t.Fatal("image message was not retained as pending")
	}
	model := a.agent.Config().Model
	model.SupportsImages = false
	a.agent.SetModel(model)
	result := a.executeOperation(context.Background(), OperationRequest{Kind: OperationRun, Value: "next"})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "pending image input") {
		t.Fatalf("result = %+v, want pending image capability error", result)
	}
	if !a.session.HasPending() {
		t.Fatal("pending image was replayed before capability check")
	}
}

func TestApplyModelSynchronizesCoordinatorModel(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.switchModel(deepseek.ProviderID, "deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	if a.coordinator.model.ID != "deepseek-v4-pro" {
		t.Fatalf("coordinator model = %q, want deepseek-v4-pro", a.coordinator.model.ID)
	}
}

func TestTreeOperationRewindsUserAndRestoresBranchSettings(t *testing.T) {
	a := operationApp(t, nil, nil)
	for _, message := range []llm.Message{textMsg("first"), assistantTreeMsg("answer")} {
		if err := a.session.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.session.AppendModelChange(deepseek.ProviderID, "deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendThinkingLevelChange(string(llm.ThinkingLevelHigh)); err != nil {
		t.Fatal(err)
	}
	for _, message := range []llm.Message{textMsg("second"), assistantTreeMsg("follow-up")} {
		if err := a.session.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	userID := a.session.Tree().Entries[4].ID
	beforeData, err := os.ReadFile(a.session.Path())
	if err != nil {
		t.Fatal(err)
	}

	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationTree, Value: userID})
	if err != nil {
		t.Fatal(err)
	}
	result := op.Wait()
	if result.Err != nil || !result.RestoreEditor || result.EditorText != "second" {
		t.Fatalf("tree result = %+v", result)
	}
	if got := a.session.LeafID(); got != a.session.Tree().Entries[3].ID {
		t.Fatalf("leaf = %q, want parent setting %q", got, a.session.Tree().Entries[3].ID)
	}
	if a.CurrentModel().ID != "deepseek-v4-pro" || a.CurrentThinkingLevel() != llm.ThinkingLevelHigh {
		t.Fatalf("branch settings = %s/%s %q", a.CurrentModel().Provider, a.CurrentModel().ID, a.CurrentThinkingLevel())
	}
	if got := len(a.ContextView().Messages()); got != 2 {
		t.Fatalf("context messages = %d, want first user and assistant parent", got)
	}
	afterData, err := os.ReadFile(a.session.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(afterData) != string(beforeData) {
		t.Fatalf("tree rewind changed persisted session: before=%d bytes after=%d", len(beforeData), len(afterData))
	}
}

func TestTreeOperationRejectsNonUserAndBusySession(t *testing.T) {
	a := operationApp(t, nil, nil)
	for _, message := range []llm.Message{textMsg("first"), assistantTreeMsg("answer")} {
		if err := a.session.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	assistantID := a.session.Tree().Entries[1].ID
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationTree, Value: assistantID})
	if err != nil {
		t.Fatal(err)
	}
	if result := op.Wait(); result.Err == nil || a.session.LeafID() != assistantID {
		t.Fatalf("assistant tree result = %+v, leaf = %q", result, a.session.LeafID())
	}

	started, release := make(chan struct{}), make(chan struct{})
	a = operationApp(t, func(ctx context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		events := make(chan llm.AssistantMessageEvent)
		close(started)
		<-release
		close(events)
		return events, nil
	}, nil)
	// The stream stub above only exists to hold the operation; Start itself is
	// the boundary under test, so the concurrent tree request must be rejected.
	run, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "busy"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := a.Start(context.Background(), OperationRequest{Kind: OperationTree, Value: "missing"}); err == nil {
		t.Fatal("tree operation accepted while run was active")
	}
	close(release)
	run.Wait()
}

func assistantTreeMsg(text string) llm.Message {
	return llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: text}}, Provider: deepseek.ProviderID, Model: deepseek.DefaultModel}
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
	a.agent.SetTools([]llm.Tool{tool})
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
	if calls != 0 || llm.CurrentSystemMessage(a.agent.Context().Messages).Sections["preamble"] != "old" {
		t.Fatal("failed patch changed request state")
	}
	restore()
	if _, err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || llm.CurrentSystemMessage(a.agent.Context().Messages).Sections["preamble"] != "new" {
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
