package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

func TestStorePendingMessagesFIFOAndNotificationDeduplication(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "inspect", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	first := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}
	second := llm.UserMessage{Content: []llm.Content{llm.ImageContent{Data: "image", MIMEType: "image/png"}}}
	if err := store.EnqueuePendingMessage(id, first); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueuePendingMessage(id, second); err != nil {
		t.Fatal(err)
	}
	messages := store.DrainPendingMessages(id, token)
	if len(messages) != 2 || messages[0].Content[0].(llm.TextContent).Text != "first" {
		t.Fatalf("pending messages = %#v, want FIFO batch", messages)
	}
	if got := store.DrainPendingMessages(id, token); got != nil {
		t.Fatalf("second drain = %#v, want empty", got)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	notification := Notification{TaskID: id, Status: StatusCompleted, Summary: "done"}
	if !store.EnqueueNotification(id, token, notification) || store.EnqueueNotification(id, token, notification) {
		t.Fatal("notification should be enqueued once")
	}
	got := store.DrainNotifications()
	if len(got) != 1 || got[0].TaskID != id {
		t.Fatalf("notifications = %#v, want one notification", got)
	}
}

func TestRecordAgentMessagesTracksCommittedCountOnly(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "inspect", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	first := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}
	if !store.RecordAgentMessages(id, token, []llm.Message{first}) {
		t.Fatal("record for the current lifecycle was rejected")
	}
	// transcript 常驻磁盘,内存只记录已提交的消息数
	if got := store.Snapshot()[0].(*AgentState).MessageCount; got != 1 {
		t.Fatalf("message count = %d, want 1", got)
	}
	store.RecordAgentMessages(id, token, []llm.Message{first, llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}})
	if got := store.Snapshot()[0].(*AgentState).MessageCount; got != 2 {
		t.Fatalf("message count after append = %d, want 2", got)
	}
	// 过期 lifecycle 的回调不得改写当前计数
	if store.RecordAgentMessages(id, token+1, nil) {
		t.Fatal("stale lifecycle callback updated the message count")
	}
	if got := store.Snapshot()[0].(*AgentState).MessageCount; got != 2 {
		t.Fatalf("message count after stale callback = %d, want 2", got)
	}
}

func TestFreshStoresDoNotReuseTaskIDs(t *testing.T) {
	first := NewStore()
	second := NewStore()
	id1, _, err := first.Register(RegisterSpec{Type: TypeAgent, Description: "first", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := second.Register(RegisterSpec{Type: TypeAgent, Description: "second", AgentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("fresh stores reused task ID %q", id1)
	}
}

func TestRegisterContinuationRejectsStaleLifecycleCallbacks(t *testing.T) {
	store := NewStore()
	id, oldToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(id, oldToken, &AgentResult{AgentID: id}) {
		t.Fatal("initial completion failed")
	}
	runtime := &AgentRuntime{Agent: agent.NewAgent(llm.AgentConfig{}, llm.Context{}, nil, nil), Done: make(chan struct{})}
	newToken, err := store.RegisterContinuation(id, runtime, "continue")
	if err != nil {
		t.Fatal(err)
	}
	if newToken == oldToken {
		t.Fatal("continuation reused lifecycle token")
	}
	if store.CompleteAgent(id, oldToken, &AgentResult{AgentID: id}) {
		t.Fatal("stale completion changed the replacement lifecycle")
	}
	if !store.CompleteAgent(id, newToken, &AgentResult{AgentID: id}) {
		t.Fatal("replacement completion was rejected")
	}
}

func TestAgentTaskStopLeavesNotificationForPartialResult(t *testing.T) {
	store := NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	id, token, err := store.Register(RegisterSpec{
		Type:        TypeAgent,
		Description: "long task",
		AgentType:   "Work",
		Runtime:     &AgentRuntime{Cancel: cancel, Done: make(chan struct{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := KillAgent(id, store); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("TaskStop did not cancel agent context")
	}
	task, _ := store.Task(id)
	if task.Base().Status != StatusKilled || task.Base().Notified {
		t.Fatalf("task after stop = %#v, want killed and notification eligible", task.Base())
	}
	if !store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusKilled}) {
		t.Fatal("Agent TaskStop should retain terminal notification")
	}
}

func TestContinuationWaitsForOldRuntime(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	done := make(chan struct{})
	runtime := &AgentRuntime{Done: done}
	store.mu.Lock()
	store.runtimes[id] = runtime
	store.mu.Unlock()
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(done)
	}()
	old, _, err := store.ContinuationRuntime(id)
	if err != nil || old != runtime {
		t.Fatalf("continuation runtime = %p, %v; want %p", old, err, runtime)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old runtime did not finish")
	}
}

func TestStoreEnterViewTracksViewingTaskWithoutLoadingTranscript(t *testing.T) {
	store := NewStore()
	id, _, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "view", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	// EnterView 只是展示层元数据:没有 OutputFile 也成功,且不触碰磁盘
	if !store.EnterView(id) {
		t.Fatal("EnterView rejected a registered task")
	}
	if got := store.ViewingTaskID(); got != id {
		t.Fatalf("viewing task = %q, want %q", got, id)
	}
	store.ExitView()
	if got := store.ViewingTaskID(); got != "" {
		t.Fatalf("viewing task after exit = %q, want empty", got)
	}
}

// transcript 常驻磁盘:ReadTaskMessages 按偏移增量返回,不缓存完整历史
func TestStoreReadTaskMessagesIsIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	manager := session.NewAt("/project", path)
	if err := manager.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	id, _, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "view", AgentType: "Work", OutputFile: path})
	if err != nil {
		t.Fatal(err)
	}
	messages, offset, err := store.ReadTaskMessages(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || offset == 0 {
		t.Fatalf("first read = %d messages, offset %d", len(messages), offset)
	}
	// 1.没有新写入时增量读取为空
	messages, next, err := store.ReadTaskMessages(id, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 || next != offset {
		t.Fatalf("idle read = %d messages, offset %d (want %d)", len(messages), next, offset)
	}
	// 2.新消息只返回增量
	if err := manager.AppendMessage(llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	messages, _, err = store.ReadTaskMessages(id, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("incremental read = %d messages, want 1", len(messages))
	}
	if assistant, ok := messages[0].(llm.AssistantMessage); !ok || assistant.Content[0].(llm.TextContent).Text != "second" {
		t.Fatalf("incremental message = %#v", messages[0])
	}
}

func TestStoreEvictKeepsQueuedNotifications(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "evict", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	// 1.运行中的任务不可淘汰
	if store.Evict(id) {
		t.Fatal("running task must not be evicted")
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted})
	// 2.终态任务淘汰后从根投影消失,但未消费的通知必须留在队列里
	if !store.Evict(id) {
		t.Fatal("terminal task was not evicted")
	}
	if _, ok := store.Task(id); ok {
		t.Fatal("evicted task is still tracked")
	}
	if got := store.DrainNotifications(); len(got) != 1 || got[0].TaskID != id {
		t.Fatalf("notifications after evict = %#v, want the undelivered notification", got)
	}
}

func TestStoreEvictExpiredReclaimsTerminalTaskAfterGracePeriod(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "expire", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted})
	task, ok := store.Task(id)
	if !ok {
		t.Fatal("completed task disappeared before its grace period")
	}
	evictAfter := task.(*AgentState).EvictAfter
	if evictAfter == nil {
		t.Fatal("terminal task has no eviction deadline")
	}
	if got := evictAfter.Sub(*task.Base().EndTime); got != panelGracePeriod {
		t.Fatalf("grace period = %v, want %v", got, panelGracePeriod)
	}
	// 1.宽限期内不驱逐
	if evicted := store.EvictExpired(); evicted != 0 {
		t.Fatalf("evicted %d tasks inside the grace period, want 0", evicted)
	}
	// 2.宽限期结束后驱逐,通知仍留在队列中
	past := time.Now().Add(-time.Second)
	store.mu.Lock()
	store.tasks[id].(*AgentState).EvictAfter = &past
	store.mu.Unlock()
	if evicted := store.EvictExpired(); evicted != 1 {
		t.Fatalf("evicted %d tasks after the grace period, want 1", evicted)
	}
	if _, ok := store.Task(id); ok {
		t.Fatal("expired task is still tracked")
	}
	if got := store.DrainNotifications(); len(got) != 1 || got[0].TaskID != id {
		t.Fatalf("notifications after expiry = %#v, want the undelivered notification", got)
	}
}

func TestStoreEvictExpiredSkipsUnnotifiedAndRetainedTasks(t *testing.T) {
	store := NewStore()
	unnotified, unnotifiedToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "unnotified", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(unnotified, unnotifiedToken, &AgentResult{AgentID: unnotified})
	retained, retainedToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "retained", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.tasks[retained].(*AgentState).Retain = true
	store.mu.Unlock()
	store.CompleteAgent(retained, retainedToken, &AgentResult{AgentID: retained})
	store.EnqueueNotification(retained, retainedToken, Notification{TaskID: retained, Status: StatusCompleted})
	if task, _ := store.Task(retained); task.(*AgentState).EvictAfter != nil {
		t.Fatal("retained task must not get an eviction deadline")
	}
	past := time.Now().Add(-time.Second)
	store.mu.Lock()
	store.tasks[unnotified].(*AgentState).EvictAfter = &past
	store.mu.Unlock()
	if evicted := store.EvictExpired(); evicted != 0 {
		t.Fatalf("evicted %d tasks, want 0 for unnotified and retained tasks", evicted)
	}
	if _, ok := store.Task(unnotified); !ok {
		t.Fatal("unnotified terminal task must survive eviction")
	}
	if _, ok := store.Task(retained); !ok {
		t.Fatal("retained terminal task must survive eviction")
	}
}

func TestStoreEvictExpiredSkipsViewedTask(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "viewed", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted})
	if !store.EnterView(id) {
		t.Fatal("could not enter the task view")
	}
	past := time.Now().Add(-time.Second)
	store.mu.Lock()
	store.tasks[id].(*AgentState).EvictAfter = &past
	store.mu.Unlock()
	// 正在阅读的 transcript 不能被清扫掉,否则面板会在中途消失
	if evicted := store.EvictExpired(); evicted != 0 {
		t.Fatalf("evicted %d viewed tasks, want 0", evicted)
	}
	if _, ok := store.Task(id); !ok {
		t.Fatal("viewed task was evicted")
	}
	// 退出视图后重新排期,宽限期过了才回收
	store.ExitView()
	if evicted := store.EvictExpired(); evicted != 0 {
		t.Fatalf("evicted %d tasks right after leaving the view, want 0", evicted)
	}
	past = time.Now().Add(-time.Second)
	store.mu.Lock()
	store.tasks[id].Base().EvictAfter = &past
	store.mu.Unlock()
	if evicted := store.EvictExpired(); evicted != 1 {
		t.Fatalf("evicted %d tasks after the grace period, want 1", evicted)
	}
	if _, ok := store.Task(id); ok {
		t.Fatal("task survived eviction after the view closed")
	}
}

func TestAgentTaskStopDuringLaunchWindowTransitionsToKilled(t *testing.T) {
	store := NewStore()
	// 注册时还没挂 runtime,复现 AttachAgentRuntime 之前的启动窗口
	id, _, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "launching", AgentType: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := StopTask(store, StopInput{TaskID: id})
	if err != nil {
		t.Fatalf("stop during launch window = %v, want success", err)
	}
	if output.TaskID != id || output.Type != TypeAgent {
		t.Fatalf("stop output = %#v, want agent task %s", output, id)
	}
	task, ok := store.Task(id)
	if !ok {
		t.Fatal("stopped task disappeared from the projection")
	}
	if task.Base().Status != StatusKilled || task.Base().EndTime == nil {
		t.Fatalf("task after launch-window stop = %#v, want killed with end time", task.Base())
	}
	if task.(*AgentState).EvictAfter == nil {
		t.Fatal("killed task has no eviction deadline")
	}
	// 没有 lifecycle 就没有通知可投递,直接视为已通知,否则该任务永远无法回收
	if !task.Base().Notified {
		t.Fatal("launch-window kill must be marked notified so it can be reclaimed")
	}
	// 迟到的 AttachAgentRuntime 必须失败,不能把已停止的任务拉回运行态
	if _, err := store.AttachAgentRuntime(id, &AgentRuntime{Cancel: func() {}}, "late"); err == nil {
		t.Fatal("late runtime attach succeeded after the task was stopped")
	}
}

// 后台 bash 任务与 agent 共用驱逐策略:终态 + 已通知 + 宽限期过后回收
func TestStoreEvictExpiredReclaimsTerminalBashTask(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeBash, Description: "sleep 1"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteBash(id, token, &BashResult{Code: 0})
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted})
	if evicted := store.EvictExpired(); evicted != 0 {
		t.Fatalf("evicted %d during the grace period, want 0", evicted)
	}
	past := time.Now().Add(-time.Second)
	store.mu.Lock()
	store.tasks[id].Base().EvictAfter = &past
	store.mu.Unlock()
	if evicted := store.EvictExpired(); evicted != 1 {
		t.Fatalf("evicted %d after the grace period, want 1", evicted)
	}
	if _, ok := store.Task(id); ok {
		t.Fatal("terminal bash task survived eviction")
	}
}

// 驱逐 bash 任务时删除它的临时输出文件;agent transcript 必须保留供 resume
func TestStoreEvictKeepsBashOutputForLateReads(t *testing.T) {
	store := NewStore()
	dir := t.TempDir()
	bashLog := filepath.Join(dir, "bash.log")
	transcript := filepath.Join(dir, "agent.jsonl")
	for _, path := range []string{bashLog, transcript} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bashID, bashToken, err := store.Register(RegisterSpec{Type: TypeBash, Description: "cmd", OutputFile: bashLog})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteBash(bashID, bashToken, &BashResult{Code: 0})
	store.EnqueueNotification(bashID, bashToken, Notification{TaskID: bashID, Status: StatusCompleted, OutputFile: bashLog})
	agentID, agentToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", OutputFile: transcript})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(agentID, agentToken, &AgentResult{AgentID: agentID})

	if !store.Evict(bashID) {
		t.Fatal("bash task was not evicted")
	}
	// 日志不随驱逐或通知确认删除:父 agent 可能在之后的轮次才去读它
	if _, err := os.Stat(bashLog); err != nil {
		t.Fatalf("bash output removed too early: %v", err)
	}
	store.AckNotifications(1)
	if _, err := os.Stat(bashLog); err != nil {
		t.Fatalf("bash output removed after ack: %v", err)
	}
	if !store.Evict(agentID) {
		t.Fatal("agent task was not evicted")
	}
	if _, err := os.Stat(transcript); err != nil {
		t.Fatalf("agent transcript must survive eviction: %v", err)
	}
}

// 子 agent 结束时回收它启动的后台 bash,避免留下没人能停的孤儿进程
func TestStoreKillBashTasksForAgent(t *testing.T) {
	store := NewStore()
	ownedID, ownedToken, err := store.Register(RegisterSpec{Type: TypeBash, Description: "owned", BashState: &BashState{ParentAgentID: "agent-1"}})
	if err != nil {
		t.Fatal(err)
	}
	otherID, otherToken, err := store.Register(RegisterSpec{Type: TypeBash, Description: "other", BashState: &BashState{ParentAgentID: "agent-2"}})
	if err != nil {
		t.Fatal(err)
	}
	if killed := store.KillBashTasksForAgent("agent-1"); killed != 1 {
		t.Fatalf("killed %d owned tasks, want 1", killed)
	}
	if task, ok := store.Task(ownedID); !ok || task.Base().Status != StatusKilled {
		t.Fatalf("owned task status = %v, want killed", task.Base().Status)
	}
	if task, ok := store.Task(otherID); !ok || task.Base().Status != StatusRunning {
		t.Fatalf("unrelated task status = %v, want running", task.Base().Status)
	}
	_ = ownedToken
	_ = otherToken
}

// 进入视图必须打上 Retain,否则用户正在读的 transcript 会被清扫掉
func TestStoreEnterViewRetainsTask(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "viewed"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted})
	if !store.EnterView(id) {
		t.Fatal("could not enter the view")
	}
	task, _ := store.Task(id)
	if !task.(*AgentState).Retain {
		t.Fatal("viewed task is not retained")
	}
	store.ExitView()
	task, _ = store.Task(id)
	if task.(*AgentState).Retain {
		t.Fatal("retain survived leaving the view")
	}
}

// 快照必须是防御性副本:改动返回值不能污染 store 内的任务状态
func TestStoreSnapshotIsolatesMutableResults(t *testing.T) {
	store := NewStore()
	bashID, bashToken, err := store.Register(RegisterSpec{Type: TypeBash, Description: "cmd"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteBash(bashID, bashToken, &BashResult{Code: 0})
	agentID, agentToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", Model: &llm.Model{ID: "m", SamplingParams: map[string]any{"t": 1.0}}})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(agentID, agentToken, &AgentResult{AgentID: agentID, Content: []TextBlock{{Text: "ok"}}})

	for _, state := range store.Snapshot() {
		switch task := state.(type) {
		case *BashState:
			task.Result.Code = 99
		case *AgentState:
			task.Result.Content[0].Text = "tampered"
			task.Model.SamplingParams["t"] = 2.0
		}
	}
	stored, _ := store.Task(bashID)
	if stored.(*BashState).Result.Code != 0 {
		t.Fatal("snapshot mutation leaked into the bash result")
	}
	agent, _ := store.Task(agentID)
	if agent.(*AgentState).Result.Content[0].Text != "ok" {
		t.Fatal("snapshot mutation leaked into the agent result")
	}
	if agent.(*AgentState).Model.SamplingParams["t"] != 1.0 {
		t.Fatal("snapshot mutation leaked into the model sampling params")
	}
}

// agent 的 sidechain transcript 不是临时产物,通知被 ack 后也不能删
func TestStoreAckKeepsAgentTranscript(t *testing.T) {
	store := NewStore()
	dir := t.TempDir()
	transcript := filepath.Join(dir, "agent.jsonl")
	if err := os.WriteFile(transcript, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", OutputFile: transcript})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted, OutputFile: transcript})
	store.AckNotifications(1)
	if _, err := os.Stat(transcript); err != nil {
		t.Fatalf("agent transcript removed after ack: %v", err)
	}
}

// 驱逐期限指针必须随快照一起克隆,否则调用方能改到 store 的调度
func TestStoreSnapshotIsolatesEvictDeadline(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	for _, state := range store.Snapshot() {
		if state.Base().EvictAfter != nil {
			*state.Base().EvictAfter = time.Time{}
		}
	}
	stored, _ := store.Task(id)
	if stored.Base().EvictAfter.IsZero() {
		t.Fatal("snapshot mutation leaked into the eviction deadline")
	}
}

// continuation 必须克隆模型,避免旧快照持有人改到新 lifecycle 的配置
func TestStoreRegisterContinuationClonesModel(t *testing.T) {
	store := NewStore()
	model := &llm.Model{ID: "m", SamplingParams: map[string]any{"t": 1.0}}
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	old, _ := store.Task(id)
	if _, err := store.RegisterContinuation(id, nil, "next"); err != nil {
		t.Fatal(err)
	}
	old.(*AgentState).Model.SamplingParams["t"] = 9.0
	current, _ := store.Task(id)
	if current.(*AgentState).Model.SamplingParams["t"] != 1.0 {
		t.Fatal("continuation shares the model with the previous state")
	}
}

// 从一个任务直接切到另一个时,旧任务的 Retain 必须释放,否则永远不会被驱逐
func TestStoreEnterViewReleasesPreviousTask(t *testing.T) {
	store := NewStore()
	first, firstToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "first"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(first, firstToken, &AgentResult{AgentID: first})
	second, secondToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "second"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(second, secondToken, &AgentResult{AgentID: second})

	store.EnterView(first)
	store.EnterView(second)

	previous, _ := store.Task(first)
	if previous.(*AgentState).Retain {
		t.Fatal("previous task kept Retain after switching views")
	}
	if previous.Base().EvictAfter == nil {
		t.Fatal("previous terminal task lost its eviction deadline")
	}
	current, _ := store.Task(second)
	if !current.(*AgentState).Retain {
		t.Fatal("current task is not retained")
	}
}

// 正在查看的任务不能被手动驱逐,否则面板会在阅读中途消失
func TestStoreEvictRejectsRetainedTask(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "viewed"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	store.EnterView(id)
	if store.Evict(id) {
		t.Fatal("retained task was evicted")
	}
	if _, ok := store.Task(id); !ok {
		t.Fatal("retained task disappeared")
	}
	store.ExitView()
	if !store.Evict(id) {
		t.Fatal("task was not evictable after leaving the view")
	}
}

// provider 参数里的嵌套容器必须一并克隆
func TestStoreSnapshotIsolatesNestedSamplingParams(t *testing.T) {
	store := NewStore()
	model := &llm.Model{ID: "m", SamplingParams: map[string]any{
		"nested": map[string]any{"list": []any{"a"}},
	}}
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(id, token, &AgentResult{AgentID: id})
	for _, state := range store.Snapshot() {
		params := state.(*AgentState).Model.SamplingParams
		params["nested"].(map[string]any)["list"].([]any)[0] = "mutated"
	}
	stored, _ := store.Task(id)
	if stored.(*AgentState).Model.SamplingParams["nested"].(map[string]any)["list"].([]any)[0] != "a" {
		t.Fatal("nested sampling params leaked out of the snapshot")
	}
}

// 主 agent 退出时必须终止所有运行中的子任务与后台命令,否则会留下孤儿进程
func TestStoreKillAllRunningTasks(t *testing.T) {
	store := NewStore()
	agentID, _, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work"})
	if err != nil {
		t.Fatal(err)
	}
	bashID, _, err := store.Register(RegisterSpec{Type: TypeBash, Description: "sleep 300"})
	if err != nil {
		t.Fatal(err)
	}
	doneID, doneToken, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "done"})
	if err != nil {
		t.Fatal(err)
	}
	store.CompleteAgent(doneID, doneToken, &AgentResult{AgentID: doneID})

	if killed := store.KillAllRunningTasks(); killed != 2 {
		t.Fatalf("killed %d tasks, want 2", killed)
	}
	for _, id := range []string{agentID, bashID} {
		task, ok := store.Task(id)
		if !ok || task.Base().Status != StatusKilled {
			t.Fatalf("task %s status = %v, want killed", id, task.Base().Status)
		}
	}
	// 已终态的任务不能被改写
	done, _ := store.Task(doneID)
	if done.Base().Status != StatusCompleted {
		t.Fatalf("completed task status = %v, want completed", done.Base().Status)
	}
}

// 子任务的 steering 与 follow-up 是两条独立队列
func TestStoreSubagentHasTwoQueues(t *testing.T) {
	store := NewStore()
	id, _, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "work"})
	if err != nil {
		t.Fatal(err)
	}
	steer := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "steer"}}}
	follow := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "follow"}}}
	if err := store.EnqueuePendingMessage(id, steer); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueFollowUpMessage(id, follow); err != nil {
		t.Fatal(err)
	}
	if got := store.PendingMessages(id); len(got) != 1 || textOf(got[0]) != "steer" {
		t.Fatalf("steering queue = %+v", got)
	}
	if got := store.PendingFollowUpMessages(id); len(got) != 1 || textOf(got[0]) != "follow" {
		t.Fatalf("follow-up queue = %+v", got)
	}
	// 取走一条不能影响另一条
	if got := store.DrainPendingMessages(id, 1); len(got) != 1 {
		t.Fatalf("drained steering = %d", len(got))
	}
	if got := store.PendingFollowUpMessages(id); len(got) != 1 {
		t.Fatal("draining steering cleared the follow-up queue")
	}
	// 清空 follow-up 不影响 steering
	if err := store.EnqueuePendingMessage(id, steer); err != nil {
		t.Fatal(err)
	}
	store.ClearFollowUpMessages(id)
	if got := store.PendingMessages(id); len(got) != 1 {
		t.Fatal("clearing follow-up cleared steering")
	}
	if got := store.PendingFollowUpMessages(id); len(got) != 0 {
		t.Fatalf("follow-up queue not cleared: %+v", got)
	}
}

func textOf(message llm.UserMessage) string {
	for _, content := range message.Content {
		if text, ok := content.(llm.TextContent); ok {
			return text.Text
		}
	}
	return ""
}
