package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
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

func TestCoordinatorForwardsBeforePersisting(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	rec := &eventRecorder{fail: true}
	coord := NewCoordinator(sm, rec.handle)

	// 转发在前：转发失败时不得落盘
	if err := coord.Handle(llm.MessageEndEvent{Message: textMsg("hi")}); err == nil {
		t.Fatal("Handle() error = nil, want forward failure propagated")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")); len(files) != 0 {
		t.Fatalf("session files = %v, want none (persist skipped after forward failure)", files)
	}
	// 转发成功后落盘
	rec.fail = false
	if err := coord.Handle(llm.MessageEndEvent{Message: textMsg("hi")}); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("session files = %v, want exactly 1", files)
	}
	reloaded, err := Open(files[0], "/project")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reloaded.BuildContext().Messages); got != 1 {
		t.Errorf("persisted messages = %d, want 1", got)
	}
}

func TestCoordinatorPersistFailureAborts(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	// 模拟残留文件使独占创建失败
	if err := os.WriteFile(sm.sessionFile, []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &eventRecorder{}
	coord := NewCoordinator(sm, rec.handle)
	if err := coord.Handle(llm.MessageEndEvent{Message: textMsg("hi")}); err == nil {
		t.Fatal("Handle() error = nil, want persist failure propagated")
	}
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

func (f *fakeRunner) ReplaceMessages(messages []llm.Message) {
	f.replaced = append(f.replaced, messages)
}

// newTestCoordinator 组装协调器与假运行器，返回摘要流计数器
func newTestCoordinator(t *testing.T, dir string, summarizerStreamFn llm.StreamFn) (*Coordinator, *fakeRunner, *eventRecorder) {
	t.Helper()
	sm := New("/project", dir)
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
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	a.AgentContext.Messages = append(a.AgentContext.Messages, seed...)
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
	sm := New("/project", dir)
	rec := &eventRecorder{}
	coord := NewCoordinator(sm, rec.handle)
	coord.Compactor = &compaction.Compactor{
		StreamFn: summaryStreamFn,
		Model:    llm.Model{ID: "m1"},
		Config:   compaction.Config{ContextWindow: 10000, ReserveTokens: 500, KeepRecentTokens: 3},
	}
	a := agent.NewAgent(llm.AgentConfig{Model: llm.Model{ID: "m1"}}, llm.Context{}, modelStreamFn, coord.Handle)
	a.Coordinator = coord
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
		if obj.Type == TypeContextEdit && obj.Replacement == nil {
			hasOmission = true
		}
		if obj.Type == TypeCompaction {
			hasCompaction = true
		}
	}
	if !hasOmission || !hasCompaction {
		t.Fatalf("omission = %v, compaction = %v, want both persisted", hasOmission, hasCompaction)
	}
	// 重载投影：失败尝试被省略，最终消息在场
	reloaded, err := Open(files[0], "/project")
	if err != nil {
		t.Fatal(err)
	}
	ctx := reloaded.BuildContext()
	if len(ctx.Messages) != 4 {
		t.Fatalf("replayed messages = %d, want 4 (summary, a0, hi, recovered)", len(ctx.Messages))
	}
	if !strings.Contains(messageTextForTest(t, ctx.Messages[0]), summaryText) {
		t.Errorf("messages[0] = %q, want wrapped summary", messageTextForTest(t, ctx.Messages[0]))
	}
	last := ctx.Messages[len(ctx.Messages)-1].(llm.AssistantMessage)
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
	sm := New("/project", dir)
	rec := &eventRecorder{}
	coord := NewCoordinator(sm, rec.handle)
	coord.Compactor = &compaction.Compactor{
		StreamFn: summaryStreamFn,
		Model:    llm.Model{ID: "m1"},
		Config:   compaction.Config{ContextWindow: 10000, ReserveTokens: 500, KeepRecentTokens: 3},
	}
	a := agent.NewAgent(llm.AgentConfig{Model: llm.Model{ID: "m1"}}, llm.Context{}, modelStreamFn, coord.Handle)
	a.Coordinator = coord
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
	coord, runner, _ := newTestCoordinator(t, dir, summaryStreamFn)
	var ids []string
	for _, msg := range []llm.Message{textMsg("u1 content here"), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1 content"}}, Usage: llm.Usage{InputTokens: 9800}}} {
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, coord.m.leafID)
	}

	compacted, err := coord.CompactIfNeeded(context.Background())
	if err != nil || !compacted {
		t.Fatalf("compacted = %v, err = %v, want true/nil", compacted, err)
	}
	if summaryCalls != 1 {
		t.Fatalf("summary calls = %d, want 1", summaryCalls)
	}
	// FirstKeptEntryID 取自投影同序 EntryIDs：保留区间从 a1 起
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	reloaded, err := Open(files[0], "/project")
	if err != nil {
		t.Fatal(err)
	}
	var firstKept string
	for _, e := range reloaded.entries {
		if c, ok := e.(*CompactionEntry); ok {
			firstKept = c.FirstKeptEntryID
		}
	}
	if firstKept != ids[1] {
		t.Errorf("FirstKeptEntryID = %q, want %q", firstKept, ids[1])
	}
	// 内存替换为投影：[摘要, a1]
	if len(runner.replaced) == 0 {
		t.Fatal("ReplaceMessages not called")
	}
	replaced := runner.replaced[len(runner.replaced)-1]
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
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, coord.m.leafID)
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
	if err := coord.Handle(llm.MessageEndEvent{Message: textMsg("u1 content here")}); err != nil {
		t.Fatal(err)
	}
	if err := coord.Handle(llm.MessageEndEvent{Message: llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1 content"}}, Usage: llm.Usage{InputTokens: 20000}}}); err != nil {
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
	coord, runner, rec := newTestCoordinator(t, dir, summaryStreamFn)
	for _, msg := range []llm.Message{
		textMsg("u1 content"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1 content"}}, Usage: llm.Usage{InputTokens: 9800}},
	} {
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
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
	for _, e := range coord.m.entries {
		if _, ok := e.(*CompactionEntry); ok {
			t.Fatal("failed compaction must not be persisted")
		}
	}
	if len(runner.replaced) != 0 {
		t.Error("ReplaceMessages called on failure")
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
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
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
	for _, e := range coord.m.entries {
		if _, ok := e.(*CompactionEntry); ok {
			hasCompaction = true
		}
	}
	if !hasCompaction {
		t.Error("manual compaction not persisted")
	}
}

func TestCoordinatorRecoveryWithoutCompactorKeepsError(t *testing.T) {
	dir := t.TempDir()
	// 窗口未知等场景下 Compactor 为 nil：溢出按原始错误收场，失败尝试保留，不得 panic
	sm := New("/project", dir)
	rec := &eventRecorder{}
	coord := NewCoordinator(sm, rec.handle)
	modelCalls := 0
	streamFn := countingStreamFn(&modelCalls,
		llm.AssistantMessage{StopReason: llm.StopReasonError},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}, StopReason: llm.StopReasonStop},
	)
	a := agent.NewAgent(llm.AgentConfig{Model: llm.Model{ID: "m1"}}, llm.Context{}, streamFn, coord.Handle)
	a.Coordinator = coord
	coord.Agent = a
	seedHistory(t, coord, a)

	runner := &fakeRunner{coord: coord}
	runner.runFn = func() (*llm.AssistantMessage, error) {
		// 模拟真实 Agent：溢出收场时先通知编排层
		coord.NotifyOverflow()
		msg := llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "boom"}}, StopReason: llm.StopReasonError}
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
			t.Fatal(err)
		}
		coord.Handle(llm.AgentEndEvent{Messages: []llm.Message{msg}})
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
	for _, e := range coord.m.entries {
		if ce, ok := e.(*ContextEditEntry); ok && ce.Replacement == nil {
			t.Fatal("omission marker should not be added without a Compactor")
		}
	}
	// WillRetry 不得报告不会发生的重试
	coord.next = rec.handle
	coord.NotifyOverflow()
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
	// 预置可压缩的历史，让恢复流程走到摘要调用
	for _, msg := range []llm.Message{textMsg("u1 content"), textMsg("u2 content")} {
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	runner.runFn = func() (*llm.AssistantMessage, error) {
		// 可恢复的长度截断：正常返回无错误，恢复失败的原因必须透出
		coord.NotifyOverflow()
		msg := llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "truncated"}}, StopReason: llm.StopReasonLength, Usage: llm.Usage{OutputTokens: 50}}
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
			t.Fatal(err)
		}
		coord.Handle(llm.AgentEndEvent{Messages: []llm.Message{msg}})
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
