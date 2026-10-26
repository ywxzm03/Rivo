package task

import (
	"errors"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// newAgentTask 注册一个运行中的 agent 任务,返回 ID 与 lifecycle token
func newAgentTask(t *testing.T, store *Store, description string) (string, uint64) {
	t.Helper()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: description, AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

// 通知入队必须原子去重:同一 lifecycle 只能投递一次
func TestEnqueueNotificationDeduplicates(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if !store.CompleteAgent(id, token, &AgentResult{AgentID: id}) {
		t.Fatal("complete failed")
	}
	if !store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted}) {
		t.Fatal("first enqueue rejected")
	}
	if store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted}) {
		t.Fatal("duplicate enqueue accepted")
	}
	if got := len(store.PeekNotifications()); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
	}
}

// 过期 token 的回调不能污染新一轮 lifecycle
func TestEnqueueNotificationRejectsStaleToken(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if !store.CompleteAgent(id, token, &AgentResult{AgentID: id}) {
		t.Fatal("complete failed")
	}
	if store.EnqueueNotification(id, token+1, Notification{TaskID: id, Status: StatusCompleted}) {
		t.Fatal("stale token accepted")
	}
	if got := len(store.PeekNotifications()); got != 0 {
		t.Fatalf("notifications = %d, want 0", got)
	}
}

// 仍在运行的任务没有终态通知资格
func TestEnqueueNotificationRejectsRunningTask(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted}) {
		t.Fatal("running task accepted a notification")
	}
}

// 通知入队必须触发唤醒回调,且回调在锁外执行
func TestEnqueueNotificationInvokesHookOutsideLock(t *testing.T) {
	store := NewStore()
	woke := make(chan struct{}, 1)
	store.SetNotifyHook(func() {
		// 回调里能再次拿锁,说明它确实在锁外执行
		store.PeekNotifications()
		woke <- struct{}{}
	})
	id, token := newAgentTask(t, store, "child")
	if !store.CompleteAgent(id, token, &AgentResult{AgentID: id}) {
		t.Fatal("complete failed")
	}
	if !store.EnqueueNotification(id, token, Notification{TaskID: id, Status: StatusCompleted}) {
		t.Fatal("enqueue failed")
	}
	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("notify hook was not invoked")
	}
}

// follow-up 队列必须独立于 steering 队列
func TestDrainPendingFollowUpMessages(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if err := store.EnqueueFollowUpMessage(id, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "later"}}}); err != nil {
		t.Fatal(err)
	}
	if got := store.DrainPendingMessages(id, token); len(got) != 0 {
		t.Fatalf("steering drain returned %d, want 0", len(got))
	}
	got := store.DrainPendingFollowUpMessages(id, token)
	if len(got) != 1 {
		t.Fatalf("follow-up drain returned %d, want 1", len(got))
	}
	// 取走后队列必须为空
	if again := store.DrainPendingFollowUpMessages(id, token); len(again) != 0 {
		t.Fatalf("second drain returned %d, want 0", len(again))
	}
}

// 撤回操作必须清空对应队列
func TestClearSteeringMessages(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if err := store.EnqueuePendingMessage(id, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "steer"}}}); err != nil {
		t.Fatal(err)
	}
	store.ClearSteeringMessages(id)
	if got := store.DrainPendingMessages(id, token); len(got) != 0 {
		t.Fatalf("steering = %d after clear, want 0", len(got))
	}
}

// 未投递消息回队必须放在队首,保证顺序
func TestRequeuePendingMessagesFront(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if err := store.EnqueuePendingMessage(id, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	requeued := []llm.UserMessage{{Content: []llm.Content{llm.TextContent{Text: "first"}}}}
	if !store.RequeuePendingMessagesFront(id, token, requeued) {
		t.Fatal("requeue rejected")
	}
	got := store.DrainPendingMessages(id, token)
	if len(got) != 2 {
		t.Fatalf("messages = %d, want 2", len(got))
	}
	if text, ok := got[0].Content[0].(llm.TextContent); !ok || text.Text != "first" {
		t.Fatalf("first message = %#v, want requeued front", got[0])
	}
}

// 过期 token 的回队请求必须被拒绝
func TestRequeuePendingMessagesRejectsStaleToken(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if store.RequeuePendingMessagesFront(id, token+1, []llm.UserMessage{{}}) {
		t.Fatal("stale token requeue accepted")
	}
	if store.RequeuePendingFollowUpMessagesFront(id, token+1, []llm.UserMessage{{}}) {
		t.Fatal("stale token follow-up requeue accepted")
	}
}

// bash 输出统计必须按 token 校验
func TestSetBashOutputStats(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeBash, Description: "cmd"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.SetBashOutputStats(id, token, true, 4096) {
		t.Fatal("stats rejected")
	}
	if store.SetBashOutputStats(id, token+1, false, 0) {
		t.Fatal("stale token stats accepted")
	}
	state, ok := store.Task(id)
	if !ok {
		t.Fatal("task missing")
	}
	bash := state.(*BashState)
	if !bash.OutputTruncated || bash.OutputBytes != 4096 {
		t.Fatalf("stats = %+v, want truncated 4096", bash)
	}
}

// bash 失败必须转 failed 并保留错误结果
func TestFailBash(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeBash, Description: "cmd"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.FailBash(id, token, &BashResult{Code: 2}, errors.New("exit 2")) {
		t.Fatal("fail rejected")
	}
	state, ok := store.Task(id)
	if !ok {
		t.Fatal("task missing")
	}
	if state.Base().Status != StatusFailed {
		t.Fatalf("status = %q, want failed", state.Base().Status)
	}
}

// runtime 元数据只写入非空字段,不覆盖已有值
func TestSetRuntimeMetadata(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if !store.SetRuntimeMetadata(id, token, &AgentRuntime{OutputFile: "/tmp/a.jsonl", WorktreePath: "/tmp/wt"}) {
		t.Fatal("metadata rejected")
	}
	if store.SetRuntimeMetadata(id, token+1, &AgentRuntime{OutputFile: "/tmp/b.jsonl"}) {
		t.Fatal("stale token metadata accepted")
	}
	state, _ := store.Task(id)
	if state.Base().OutputFile != "/tmp/a.jsonl" {
		t.Fatalf("output file = %q", state.Base().OutputFile)
	}
	// 空值不能覆盖已写入的路径
	store.SetRuntimeMetadata(id, token, &AgentRuntime{})
	state, _ = store.Task(id)
	if state.Base().OutputFile != "/tmp/a.jsonl" {
		t.Fatalf("empty metadata overwrote output file: %q", state.Base().OutputFile)
	}
}

// worktree 路径清理后必须为空,过期 token 拒绝
func TestClearWorktreePath(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "child", AgentType: "work", WorktreePath: "/tmp/wt"})
	if err != nil {
		t.Fatal(err)
	}
	if store.ClearWorktreePath(id, token+1) {
		t.Fatal("stale token clear accepted")
	}
	if !store.ClearWorktreePath(id, token) {
		t.Fatal("clear rejected")
	}
	state, _ := store.Task(id)
	if agent := state.(*AgentState); agent.WorktreePath != "" {
		t.Fatalf("worktree path = %q, want empty", agent.WorktreePath)
	}
}

// 启动失败占位的任务必须能整条丢弃,不留终态记录
func TestDiscardReserved(t *testing.T) {
	store := NewStore()
	id, _ := newAgentTask(t, store, "child")
	if !store.DiscardReserved(id) {
		t.Fatal("discard rejected")
	}
	if _, ok := store.Task(id); ok {
		t.Fatal("reserved task survived discard")
	}
	if store.DiscardReserved(id) {
		t.Fatal("discard of missing task accepted")
	}
}

// RemoveRuntime 只删除匹配 token 的 runtime 句柄
func TestRemoveRuntime(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if _, err := store.AttachAgentRuntime(id, &AgentRuntime{}, "prompt"); err != nil {
		t.Fatal(err)
	}
	// 过期 token 不能移除当前 runtime
	store.RemoveRuntime(id, token+1)
	if _, ok := store.runtimes[id]; !ok {
		t.Fatal("stale token removed the runtime")
	}
	store.RemoveRuntime(id, token)
	if _, ok := store.runtimes[id]; ok {
		t.Fatal("runtime survived removal")
	}
}

// AttachAgentRuntime 拒绝非运行态任务
func TestAttachAgentRuntimeRejectsTerminalTask(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if !store.CompleteAgent(id, token, &AgentResult{AgentID: id}) {
		t.Fatal("complete failed")
	}
	if _, err := store.AttachAgentRuntime(id, &AgentRuntime{}, "prompt"); err == nil {
		t.Fatal("terminal task accepted a runtime")
	}
}

// Error 在失败任务上返回错误文本,正常任务返回空
func TestAgentStateError(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	state, _ := store.Task(id)
	if got := state.(*AgentState).Error; got != "" {
		t.Fatalf("running task error = %q, want empty", got)
	}
	if !store.FailAgent(id, token, errors.New("boom")) {
		t.Fatal("fail rejected")
	}
	state, _ = store.Task(id)
	if got := state.(*AgentState).Error; got != "boom" {
		t.Fatalf("error = %q, want boom", got)
	}
}
