package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// eventRecorder 记录转发的事件
type eventRecorder struct {
	events []llm.AgentEvent
	fail   bool
}

func (r *eventRecorder) handle(event llm.AgentEvent) error {
	if r.fail {
		return errors.New("display failed")
	}
	r.events = append(r.events, event)
	return nil
}

func (r *eventRecorder) agentEnds() []llm.AgentEndEvent {
	var ends []llm.AgentEndEvent
	for _, e := range r.events {
		if end, ok := e.(llm.AgentEndEvent); ok {
			ends = append(ends, end)
		}
	}
	return ends
}

// countingStreamFn 造一个按调用次数返回预设回复的假流，供模型调用与摘要调用共用
func countingStreamFn(calls *int, replies ...llm.AssistantMessage) llm.StreamFn {
	return func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		i := *calls
		*calls++
		ch := make(chan llm.AssistantMessageEvent, 1)
		reply := replies[min(i, len(replies)-1)]
		if reply.StopReason == llm.StopReasonError && len(reply.Content) == 0 {
			ch <- llm.AssistantMessageErrorEvent{Err: fmt.Errorf("openai: 400: %w", llm.ErrContextOverflow)}
		} else {
			ch <- llm.AssistantMessageDoneEvent{Message: &reply}
		}
		close(ch)
		return ch, nil
	}
}

// summaryText 用作摘要回复的固定文本
const summaryText = "COMPACT-SUMMARY"

func textMsg(text string) llm.Message {
	return llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}}
}

func TestCoordinatorNotificationDoesNotOwnPersistence(t *testing.T) {
	sm := session.New("/project", t.TempDir())
	rec := &eventRecorder{fail: true}
	coord := NewCoordinator(sm, rec.handle)
	if _, err := coord.CommitMessages([]llm.Message{textMsg("hi")}); err != nil {
		t.Fatal(err)
	}
	if err := coord.Handle(llm.MessageEndEvent{Message: textMsg("hi")}); err == nil {
		t.Fatal("expected display failure")
	}
	reloaded, err := session.Open(sm.Path(), "/project")
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.BuildContext().Messages()) != 1 {
		t.Fatal("display failure changed committed records")
	}
}

func TestCoordinatorRejectsSteeringDuringCompaction(t *testing.T) {
	coord, runner, _ := newTestCoordinator(t, t.TempDir(), nil)
	coord.compacting = true
	if err := coord.EnqueueSteering("during compact"); !errors.Is(err, ErrSteeringDuringCompaction) {
		t.Fatalf("EnqueueSteering() error = %v, want compaction error", err)
	}
	if len(runner.appended) != 0 {
		t.Fatal("steering reached runner during compaction")
	}
	coord.compacting = false
	if err := coord.EnqueueSteering("after compact"); err != nil {
		t.Fatal(err)
	}
	if len(runner.appended) != 1 || messageTextForTest(t, runner.appended[0]) != "after compact" {
		t.Fatalf("runner appended = %+v", runner.appended)
	}
}

func TestCoordinatorRejectsImagesForLegacyRunner(t *testing.T) {
	coord, _, _ := newTestCoordinator(t, t.TempDir(), nil)
	message := llm.UserMessage{Content: []llm.Content{
		llm.TextContent{Text: "describe"},
		llm.ImageContent{Data: "aGk=", MIMEType: "image/png"},
	}}
	if _, err := coord.RunMessage(context.Background(), message); err == nil {
		t.Fatal("RunMessage accepted image input through a text-only runner")
	}
	if err := coord.EnqueueSteeringMessage(message); err == nil {
		t.Fatal("EnqueueSteeringMessage accepted image input through a text-only runner")
	}
}

func TestCoordinatorContinueQueuedRequiresQueuedRunner(t *testing.T) {
	coord, runner, _ := newTestCoordinator(t, t.TempDir(), nil)
	if _, err := coord.ContinueQueued(context.Background()); !errors.Is(err, ErrQueuedRunnerUnavailable) {
		t.Fatalf("ContinueQueued() error = %v, want queued runner error", err)
	}
	if runner.continueCalls != 0 {
		t.Fatalf("Continue() calls = %d, want none", runner.continueCalls)
	}
}

func TestCoordinatorPersistFailureAborts(t *testing.T) {
	sm := session.New("/project", t.TempDir())
	if err := os.WriteFile(sm.Path(), []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	coord := NewCoordinator(sm, nil)
	if _, err := coord.CommitMessages([]llm.Message{textMsg("hi")}); err == nil {
		t.Fatal("expected commit failure")
	}
	if len(sm.BuildContext().Messages()) != 0 {
		t.Fatal("failed commit published context")
	}
}

// sessionEntries 通过磁盘记录核对宿主写入的 entry 类型
func sessionEntries(t *testing.T, sm *session.SessionManager) []session.Entry {
	t.Helper()
	data, err := os.ReadFile(sm.Path())
	if err != nil {
		t.Fatal(err)
	}
	var entries []session.Entry
	for _, line := range bytes.Split(data, []byte("\n"))[1:] {
		if len(line) == 0 {
			continue
		}
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &kind); err != nil {
			t.Fatal(err)
		}
		if kind.Type == session.TypeCompaction {
			var e session.CompactionEntry
			if err := json.Unmarshal(line, &e); err != nil {
				t.Fatal(err)
			}
			entries = append(entries, &e)
		} else if kind.Type == session.TypeContextEdit {
			var e session.ContextEditEntry
			if err := json.Unmarshal(line, &e); err != nil {
				t.Fatal(err)
			}
			entries = append(entries, &e)
		}
	}
	return entries
}

// fakeRunner 记录编排层调用的假运行器
type fakeRunner struct {
	coord         *Coordinator
	replaced      [][]llm.Message
	appended      []llm.Message
	continueCalls int
	// runFn/contFn 非空时定制 Run/Continue 行为
	runFn  func() (*llm.AssistantMessage, error)
	contFn func() (*llm.AssistantMessage, error)
}

// AppendMessage 记录编排层压入的 system prompt 补丁
func (f *fakeRunner) AppendMessage(msg llm.Message) {
	f.appended = append(f.appended, msg)
}

func (f *fakeRunner) Run(_ context.Context, _ string) (*llm.AssistantMessage, error) {
	if f.runFn != nil {
		return f.runFn()
	}
	return nil, nil
}

func (f *fakeRunner) Continue(_ context.Context) (*llm.AssistantMessage, error) {
	f.continueCalls++
	if f.contFn != nil {
		return f.contFn()
	}
	return nil, nil
}

func (f *fakeRunner) Steer(input string) error {
	f.appended = append(f.appended, textMsg(input))
	return nil
}

func (f *fakeRunner) FollowUp(input string) error {
	f.appended = append(f.appended, textMsg(input))
	return nil
}

func (f *fakeRunner) ReplaceMessages(messages []llm.Message) {
	f.replaced = append(f.replaced, messages)
}

// newTestCoordinator 组装协调器与假运行器，返回摘要流计数器
func newTestCoordinator(t *testing.T, dir string, summarizerStreamFn llm.StreamFn) (*Coordinator, *fakeRunner, *eventRecorder) {
	t.Helper()
	sm := session.New("/project", dir)
	rec := &eventRecorder{}
	coord := NewCoordinator(sm, rec.handle)
	if summarizerStreamFn != nil {
		coord.Compactor = &compaction.Compactor{
			StreamFn: summarizerStreamFn,
			Model:    llm.Model{ID: "m1"},
			Config:   compaction.Config{ContextWindow: 10000, ReserveTokens: 500, KeepRecentTokens: 3},
			Retry:    compaction.RetryPolicy{},
		}
	}
	runner := &fakeRunner{}
	coord.Agent = runner
	return coord, runner, rec
}

// seedHistory 预置一轮可压缩的历史：先落盘再入内存，保持两边一致
func seedHistory(t *testing.T, coord *Coordinator, a *agent.Agent) {
	t.Helper()
	seed := []llm.Message{
		textMsg("earlier context"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "earlier reply"}}},
	}
	for _, msg := range seed {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	a.ReplaceMessages(append(a.Context().Messages, seed...))
}

func TestCoordinatorOverflowRecovery(t *testing.T) {
	dir := t.TempDir()
	modelCalls, summaryCalls := 0, 0
	modelStreamFn := countingStreamFn(&modelCalls,
		llm.AssistantMessage{StopReason: llm.StopReasonError},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "recovered"}}, StopReason: llm.StopReasonStop},
	)
	summaryStreamFn := countingStreamFn(&summaryCalls,
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
	)
	sm := session.New("/project", dir)
	rec := &eventRecorder{}
	coord := NewCoordinator(sm, rec.handle)
	coord.Compactor = &compaction.Compactor{
		StreamFn: summaryStreamFn,
		Model:    llm.Model{ID: "m1"},
		Config:   compaction.Config{ContextWindow: 10000, ReserveTokens: 500, KeepRecentTokens: 3},
	}
	a := agent.New(agent.Options{
		Config:  llm.AgentConfig{Model: llm.Model{ID: "m1"}},
		Context: llm.Context{},
		Stream:  modelStreamFn,
		Events:  coord.Handle,
		Hooks:   agent.Hooks{PrepareRequest: coord.PrepareRequest, CommitMessages: coord.CommitMessages},
	})
	coord.Agent = a
	seedHistory(t, coord, a)

	reply, err := coord.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run() error = %v, want recovery", err)
	}
	if text := reply.Content[0].(llm.TextContent).Text; text != "recovered" {
		t.Errorf("reply = %q, want recovered", text)
	}
	if modelCalls != 2 || summaryCalls != 1 {
		t.Fatalf("model calls = %d, summary calls = %d, want 2 and 1", modelCalls, summaryCalls)
	}
	// agent_end 标注：第一段 willRetry=true，续接段 false
	ends := rec.agentEnds()
	if len(ends) != 2 {
		t.Fatalf("agent_end events = %d, want 2", len(ends))
	}
	if !ends[0].WillRetry {
		t.Error("first agent_end WillRetry = false, want true")
	}
	if ends[1].WillRetry {
		t.Error("second agent_end WillRetry = true, want false")
	}
	// 会话文件：省略标记 + 压缩点落盘
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("session files = %v, want exactly 1", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	hasOmission, hasCompaction := false, false
	for _, line := range bytes.Split(data, []byte("\n")) {
		var obj struct {
			Type        string           `json:"type"`
			Replacement *json.RawMessage `json:"replacement"`
		}
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		if obj.Type == session.TypeContextEdit && obj.Replacement == nil {
			hasOmission = true
		}
		if obj.Type == session.TypeCompaction {
			hasCompaction = true
		}
	}
	if !hasOmission || !hasCompaction {
		t.Fatalf("omission = %v, compaction = %v, want both persisted", hasOmission, hasCompaction)
	}
	// 重载投影：失败尝试被省略，最终消息在场
	reloaded, err := session.Open(files[0], "/project")
	if err != nil {
		t.Fatal(err)
	}
	ctx := reloaded.BuildContext()
	if len(ctx.Messages()) != 4 {
		t.Fatalf("replayed messages = %d, want 4 (summary, a0, hi, recovered)", len(ctx.Messages()))
	}
	if !strings.Contains(messageTextForTest(t, ctx.Messages()[0]), summaryText) {
		t.Errorf("messages[0] = %q, want wrapped summary", messageTextForTest(t, ctx.Messages()[0]))
	}
	last := ctx.Messages()[len(ctx.Messages())-1].(llm.AssistantMessage)
	if text := last.Content[0].(llm.TextContent).Text; text != "recovered" {
		t.Errorf("final = %q, want recovered", text)
	}
}

func messageTextForTest(t *testing.T, msg llm.Message) string {
	t.Helper()
	switch m := msg.(type) {
	case llm.UserMessage:
		text, _ := m.Content[0].(llm.TextContent)
		return text.Text
	case llm.AssistantMessage:
		text, _ := m.Content[0].(llm.TextContent)
		return text.Text
	}
	t.Fatalf("unexpected message %#v", msg)
	return ""
}

func TestCoordinatorOverflowRecoveryOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	modelCalls, summaryCalls := 0, 0
	modelStreamFn := countingStreamFn(&modelCalls,
		llm.AssistantMessage{StopReason: llm.StopReasonError},
		llm.AssistantMessage{StopReason: llm.StopReasonError},
	)
	summaryStreamFn := countingStreamFn(&summaryCalls,
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}},
	)
	sm := session.New("/project", dir)
	rec := &eventRecorder{}
	coord := NewCoordinator(sm, rec.handle)
	coord.Compactor = &compaction.Compactor{
		StreamFn: summaryStreamFn,
		Model:    llm.Model{ID: "m1"},
		Config:   compaction.Config{ContextWindow: 10000, ReserveTokens: 500, KeepRecentTokens: 3},
	}
	a := agent.New(agent.Options{
		Config:  llm.AgentConfig{Model: llm.Model{ID: "m1"}},
		Context: llm.Context{},
		Stream:  modelStreamFn,
		Events:  coord.Handle,
		Hooks:   agent.Hooks{PrepareRequest: coord.PrepareRequest, CommitMessages: coord.CommitMessages},
	})
	coord.Agent = a
	seedHistory(t, coord, a)

	_, err := coord.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("second overflow should fail the run")
	}
	if summaryCalls != 1 {
		t.Errorf("summary calls = %d, want 1 (recovery only once)", summaryCalls)
	}
}

func TestCoordinatorThresholdCompaction(t *testing.T) {
	dir := t.TempDir()
	summaryCalls := 0
	summaryStreamFn := countingStreamFn(&summaryCalls,
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
	)
	coord, _, _ := newTestCoordinator(t, dir, summaryStreamFn)
	var ids []string
	for _, msg := range []llm.Message{textMsg("u1 content here"), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1 content"}}, Usage: llm.Usage{InputTokens: 9800}}} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, coord.m.LastEntryID())
	}

	compacted, err := coord.CompactIfNeeded(context.Background())
	if err != nil || !compacted {
		t.Fatalf("compacted = %v, err = %v, want true/nil", compacted, err)
	}
	if summaryCalls != 1 {
		t.Fatalf("summary calls = %d, want 1", summaryCalls)
	}
	// FirstKeptEntryID 取自结构化上下文条目的来源 ID：保留区间从 a1 起
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	reloaded, err := session.Open(files[0], "/project")
	if err != nil {
		t.Fatal(err)
	}
	var firstKept string
	for _, e := range sessionEntries(t, reloaded) {
		if c, ok := e.(*session.CompactionEntry); ok {
			firstKept = c.FirstKeptEntryID
		}
	}
	if firstKept != ids[1] {
		t.Errorf("FirstKeptEntryID = %q, want %q", firstKept, ids[1])
	}
	// 内存替换为投影：[摘要, a1]
	replaced := coord.m.BuildContext().Messages()
	if len(replaced) != 2 {
		t.Errorf("replaced messages = %d, want 2 (summary, a1)", len(replaced))
	}
}

func TestCoordinatorStaleUsageUsesEstimate(t *testing.T) {
	dir := t.TempDir()
	summaryCalls := 0
	summaryStreamFn := countingStreamFn(&summaryCalls,
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
	)
	coord, _, _ := newTestCoordinator(t, dir, summaryStreamFn)
	var ids []string
	for _, msg := range []llm.Message{textMsg("u1"), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1"}}, Usage: llm.Usage{InputTokens: 9800}}} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, coord.m.LastEntryID())
	}
	if err := coord.m.AppendCompaction(summaryText, ids[1], 9804, nil, nil); err != nil {
		t.Fatal(err)
	}
	// 陈旧 usage 期间改用纯估算：小投影不再触发第二次压缩
	compacted, err := coord.CompactIfNeeded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if compacted {
		t.Error("compacted = true, want false (stale usage falls back to estimate)")
	}
	if summaryCalls != 0 {
		t.Errorf("summary calls = %d, want 0", summaryCalls)
	}
}

func TestCoordinatorSilentOverflowReason(t *testing.T) {
	dir := t.TempDir()
	summaryCalls := 0
	summaryStreamFn := countingStreamFn(&summaryCalls,
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}},
	)
	coord, _, rec := newTestCoordinator(t, dir, summaryStreamFn)
	// 最后一条 assistant 的 usage 超窗：静默溢出，原因记为 overflow
	if err := coord.m.AppendMessage(textMsg("u1 content here")); err != nil {
		t.Fatal(err)
	}
	if err := coord.m.AppendMessage(llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1 content"}}, Usage: llm.Usage{InputTokens: 20000}}); err != nil {
		t.Fatal(err)
	}
	compacted, err := coord.CompactIfNeeded(context.Background())
	if err != nil || !compacted {
		t.Fatalf("compacted = %v, err = %v, want true/nil", compacted, err)
	}
	var start *llm.CompactionStartEvent
	for _, e := range rec.events {
		if s, ok := e.(llm.CompactionStartEvent); ok {
			start = &s
		}
	}
	if start == nil || start.Reason != CompactionReasonOverflow {
		t.Errorf("compaction start = %+v, want reason overflow", start)
	}
}

func TestCoordinatorCompactFailureKeepsMemory(t *testing.T) {
	dir := t.TempDir()
	summaryCalls := 0
	summaryStreamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		summaryCalls++
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageErrorEvent{Err: errors.New("provider down")}
		close(ch)
		return ch, nil
	}
	coord, _, rec := newTestCoordinator(t, dir, summaryStreamFn)
	for _, msg := range []llm.Message{
		textMsg("u1 content"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1 content"}}, Usage: llm.Usage{InputTokens: 9800}},
	} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	_, err := coord.CompactIfNeeded(context.Background())
	if err == nil {
		t.Fatal("CompactIfNeeded() error = nil, want summarizer failure")
	}
	// 失败通知转发，内存未替换，压缩点未落盘
	if summaryCalls != 1 {
		t.Errorf("summary calls = %d, want 1", summaryCalls)
	}
	var failEvent *llm.CompactionEndEvent
	for _, e := range rec.events {
		if end, ok := e.(llm.CompactionEndEvent); ok && end.Error != "" {
			failEvent = &end
		}
	}
	if failEvent == nil {
		t.Fatal("failure event not forwarded")
	}
	for _, e := range sessionEntries(t, coord.m) {
		if _, ok := e.(*session.CompactionEntry); ok {
			t.Fatal("failed compaction must not be persisted")
		}
	}
}

func TestCoordinatorManualCompact(t *testing.T) {
	dir := t.TempDir()
	summaryCalls := 0
	var prompts []string
	summaryStreamFn := func(_ context.Context, _ llm.Model, c llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		summaryCalls++
		prompts = append(prompts, c.Messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text)
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}}}
		close(ch)
		return ch, nil
	}
	coord, _, _ := newTestCoordinator(t, dir, summaryStreamFn)
	for _, msg := range []llm.Message{textMsg("u1 content"), textMsg("u2 content")} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := coord.Compact(context.Background(), "focus on tests"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompts[0], "Additional focus: focus on tests") {
		t.Errorf("prompt = %q, want additional focus", prompts[0])
	}
	var hasCompaction bool
	for _, e := range sessionEntries(t, coord.m) {
		if _, ok := e.(*session.CompactionEntry); ok {
			hasCompaction = true
		}
	}
	if !hasCompaction {
		t.Error("manual compaction not persisted")
	}
}

// compaction 结束事件是 TUI 转发暂存输入的触发点，此时入队边界必须已经打开
func TestCoordinatorCompactionEndEventAllowsQueueEnqueue(t *testing.T) {
	dir := t.TempDir()
	summaryStreamFn := countingStreamFn(new(int), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}})
	sm := session.New("/project", dir)
	runner := &fakeRunner{}
	var steeringErr, followUpErr error
	var enqueued bool
	var coord *Coordinator
	coord = NewCoordinator(sm, func(event llm.AgentEvent) error {
		if _, ok := event.(llm.CompactionEndEvent); ok && !enqueued {
			enqueued = true
			steeringErr = coord.EnqueueSteering("staged steering")
			followUpErr = coord.EnqueueFollowUp("staged follow-up")
		}
		return nil
	})
	coord.Compactor = &compaction.Compactor{
		StreamFn: summaryStreamFn,
		Model:    llm.Model{ID: "m1"},
		Config:   compaction.Config{ContextWindow: 10000, ReserveTokens: 500, KeepRecentTokens: 3},
	}
	coord.Agent = runner
	for _, msg := range []llm.Message{textMsg("u1 content"), textMsg("u2 content")} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := coord.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if !enqueued {
		t.Fatal("compaction end event not forwarded")
	}
	if steeringErr != nil || followUpErr != nil {
		t.Fatalf("enqueue after compaction = %v / %v, want both accepted", steeringErr, followUpErr)
	}
}

func TestCoordinatorPersistsCompactionFileLists(t *testing.T) {
	dir := t.TempDir()
	summaryStreamFn := countingStreamFn(new(int), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}})
	coord, _, _ := newTestCoordinator(t, dir, summaryStreamFn)
	coord.Compactor.Config.KeepRecentTokens = 1
	for _, msg := range []llm.Message{
		textMsg("inspect files"),
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{Name: "read", Arguments: map[string]any{"path": "README.md"}},
			llm.ToolCall{Name: "edit", Arguments: map[string]any{"path": "main.go"}},
		}},
		textMsg("continue"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}},
	} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := coord.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	view := coord.m.BuildContext()
	if len(view.Entries) == 0 || view.Entries[0].Details == nil {
		t.Fatalf("compaction details = %+v, want persisted details", view.Entries)
	}
	if !strings.Contains(view.Entries[0].Summary, "<read-files>\nREADME.md\n</read-files>") || !strings.Contains(view.Entries[0].Summary, "<modified-files>\nmain.go\n</modified-files>") {
		t.Fatalf("summary = %q, want file sections", view.Entries[0].Summary)
	}
	reopened, err := session.Open(coord.m.Path(), "/project")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.BuildContext().Entries[0].Details, view.Entries[0].Details) {
		t.Fatalf("reopened details = %+v, want %+v", reopened.BuildContext().Entries[0].Details, view.Entries[0].Details)
	}
}

func TestCoordinatorCompactsTwiceWithoutReserializingPreviousSummary(t *testing.T) {
	dir := t.TempDir()
	var prompts []string
	var summaryCalls int
	summaryStreamFn := func(_ context.Context, _ llm.Model, c llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		summaryCalls++
		prompts = append(prompts, c.Messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text)
		reply := "FIRST-SUMMARY"
		if summaryCalls == 2 {
			reply = "SECOND-SUMMARY"
		}
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: reply}}}}
		close(ch)
		return ch, nil
	}
	coord, _, _ := newTestCoordinator(t, dir, summaryStreamFn)
	coord.Compactor.Config.KeepRecentTokens = 2
	if err := coord.m.AppendMessage(llm.SystemMessage{Sections: map[string]string{"preamble": "base"}}); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []llm.Message{
		textMsg("u1"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1"}}},
		textMsg("u2"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a2"}}},
	} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := coord.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []llm.Message{
		textMsg("u3"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a3"}}},
		textMsg("u4"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a4"}}},
	} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := coord.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if summaryCalls != 2 {
		t.Fatalf("summary calls = %d, want 2", summaryCalls)
	}
	if strings.Contains(prompts[1], ctxview.WrappedSummary("FIRST-SUMMARY")) {
		t.Error("second compaction prompt reserialized the previous wrapped summary")
	}
	if got := strings.Count(prompts[1], "<previous-summary>\nFIRST-SUMMARY\n</previous-summary>"); got != 1 {
		t.Errorf("previous summary block count = %d, want 1", got)
	}
	view := coord.m.BuildContext()
	if view.Entries[0].Summary != "SECOND-SUMMARY" {
		t.Errorf("latest summary = %q, want SECOND-SUMMARY", view.Entries[0].Summary)
	}
}

func TestCoordinatorRecoveryWithoutCompactorKeepsError(t *testing.T) {
	dir := t.TempDir()
	// 窗口未知等场景下 Compactor 为 nil：溢出按原始错误收场，失败尝试保留，不得 panic
	sm := session.New("/project", dir)
	rec := &eventRecorder{}
	coord := NewCoordinator(sm, rec.handle)
	modelCalls := 0
	streamFn := countingStreamFn(&modelCalls,
		llm.AssistantMessage{StopReason: llm.StopReasonError},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}, StopReason: llm.StopReasonStop},
	)
	a := agent.New(agent.Options{
		Config:  llm.AgentConfig{Model: llm.Model{ID: "m1"}},
		Context: llm.Context{},
		Stream:  streamFn,
		Events:  coord.Handle,
		Hooks:   agent.Hooks{PrepareRequest: coord.PrepareRequest, CommitMessages: coord.CommitMessages},
	})
	coord.Agent = a
	seedHistory(t, coord, a)

	runner := &fakeRunner{coord: coord}
	runner.runFn = func() (*llm.AssistantMessage, error) {
		// 模拟真实 Agent：溢出收场时先通知编排层
		coord.pendingRecovery = true
		msg := llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "boom"}}, StopReason: llm.StopReasonError}
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
		coord.Handle(llm.AgentEndEvent{Messages: []llm.Message{msg}, Err: llm.ErrContextOverflow})
		return &msg, fmt.Errorf("openai: 400: %w", llm.ErrContextOverflow)
	}
	runner.contFn = func() (*llm.AssistantMessage, error) {
		t.Error("Continue should not be called without a Compactor")
		return nil, nil
	}
	coord.Agent = runner

	reply, err := coord.Run(context.Background(), "hi")
	if !llm.IsContextOverflow(err) {
		t.Fatalf("Run() error = %v, want original overflow error", err)
	}
	if reply == nil || reply.StopReason != llm.StopReasonError {
		t.Errorf("reply = %+v, want error assistant", reply)
	}
	if runner.continueCalls != 0 {
		t.Errorf("continue calls = %d, want 0", runner.continueCalls)
	}
	// 失败尝试保留在会话中，未追加省略标记
	for _, e := range sessionEntries(t, coord.m) {
		if ce, ok := e.(*session.ContextEditEntry); ok && ce.Replacement == nil {
			t.Fatal("omission marker should not be added without a Compactor")
		}
	}
	// WillRetry 不得报告不会发生的重试
	coord.next = rec.handle
	coord.pendingRecovery = true
	if err := coord.Handle(llm.AgentEndEvent{}); err != nil {
		t.Fatal(err)
	}
	var retryEnds int
	for _, e := range rec.events {
		if end, ok := e.(llm.AgentEndEvent); ok && end.WillRetry {
			retryEnds++
		}
	}
	if retryEnds != 0 {
		t.Errorf("agent_end WillRetry should be false without a Compactor: %+v", rec.agentEnds())
	}
}

func TestCoordinatorRecoveryFailureSurfacesError(t *testing.T) {
	dir := t.TempDir()
	summaryCalls := 0
	summaryStreamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		summaryCalls++
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageErrorEvent{Err: errors.New("provider down")}
		close(ch)
		return ch, nil
	}
	coord, runner, _ := newTestCoordinator(t, dir, summaryStreamFn)
	coord.model = llm.Model{MaxTokens: 100}
	// 预置可压缩的历史，让恢复流程走到摘要调用
	for _, msg := range []llm.Message{textMsg("u1 content"), textMsg("u2 content")} {
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	runner.runFn = func() (*llm.AssistantMessage, error) {
		// 可恢复的长度截断：正常返回无错误，恢复失败的原因必须透出
		coord.pendingRecovery = true
		msg := llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "truncated"}}, StopReason: llm.StopReasonLength, Usage: llm.Usage{OutputTokens: 50}}
		if err := coord.m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
		coord.Handle(llm.AgentEndEvent{Messages: []llm.Message{msg}, Err: llm.ErrContextOverflow})
		return &msg, nil
	}

	reply, err := coord.Run(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("Run() error = %v, want summarizer failure surfaced", err)
	}
	if summaryCalls != 1 {
		t.Errorf("summary calls = %d, want 1", summaryCalls)
	}
	if reply == nil || reply.StopReason != llm.StopReasonLength {
		t.Errorf("reply = %+v, want length-stopped assistant", reply)
	}
}

func TestCoordinatorCompactSerializesWithRun(t *testing.T) {
	dir := t.TempDir()
	coord, runner, _ := newTestCoordinator(t, dir, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	runner.runFn = func() (*llm.AssistantMessage, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	}

	runDone := make(chan struct{})
	go func() {
		_, _ = coord.Run(context.Background(), "hi")
		close(runDone)
	}()
	<-started

	// Run 未结束前 Compact 必须被互斥阻塞
	compactDone := make(chan struct{})
	go func() {
		_ = coord.Compact(context.Background(), "")
		close(compactDone)
	}()
	select {
	case <-compactDone:
		t.Fatal("Compact should block while Run is in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-runDone
	select {
	case <-compactDone:
	case <-time.After(time.Second):
		t.Fatal("Compact did not complete after Run finished")
	}
}
