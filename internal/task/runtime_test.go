package task

import (
	"context"
	"errors"
	"testing"
)

// fakeRuntime 只实现 task.Runtime 接口，证明 store 不再依赖具体 agent 类型
type fakeRuntime struct {
	abort      context.CancelFunc
	done       chan struct{}
	token      uint64
	outputPath string
	worktree   string
}

func (r *fakeRuntime) Abort() context.CancelFunc { return r.abort }
func (r *fakeRuntime) Done() <-chan struct{}     { return r.done }
func (r *fakeRuntime) SetToken(token uint64)     { r.token = token }
func (r *fakeRuntime) OutputPath() string        { return r.outputPath }
func (r *fakeRuntime) Worktree() string          { return r.worktree }

// Register 必须把 lifecycle token 写回 runtime，供后续取消校验
func TestRegisterWritesTokenIntoRuntime(t *testing.T) {
	store := NewStore()
	runtime := &fakeRuntime{done: make(chan struct{})}
	id, token, err := store.Register(RegisterSpec{
		Type: TypeAgent, Description: "job", AgentType: "explore", Runtime: runtime,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if runtime.token != token {
		t.Errorf("runtime token = %d, want %d", runtime.token, token)
	}
	if got, _, err := store.ContinuationRuntime(id); err != nil || got != runtime {
		t.Errorf("ContinuationRuntime() = %v, %v; want registered runtime", got, err)
	}
}

// SetRuntimeMetadata 从 runtime 回填输出文件与 worktree 路径
func TestSetRuntimeMetadataReadsRuntimePaths(t *testing.T) {
	store := NewStore()
	runtime := &fakeRuntime{done: make(chan struct{})}
	id, token, err := store.Register(RegisterSpec{
		Type: TypeAgent, Description: "job", AgentType: "explore", Runtime: runtime,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	runtime.outputPath = "/tmp/child.jsonl"
	runtime.worktree = "/tmp/wt"
	if !store.SetRuntimeMetadata(id, token, runtime) {
		t.Fatal("SetRuntimeMetadata() = false, want true")
	}
	state, ok := store.Task(id)
	if !ok {
		t.Fatal("Task() missing after Register")
	}
	if got := state.Base().OutputFile; got != "/tmp/child.jsonl" {
		t.Errorf("OutputFile = %q, want /tmp/child.jsonl", got)
	}
	agent := state.(*AgentState)
	if agent.WorktreePath != "/tmp/wt" {
		t.Errorf("WorktreePath = %q, want /tmp/wt", agent.WorktreePath)
	}
}

// KillAgent 通过 Runtime.Abort 取消，而不是访问具体运行时字段
func TestKillAgentUsesRuntimeAbort(t *testing.T) {
	store := NewStore()
	aborted := false
	runtime := &fakeRuntime{done: make(chan struct{}), abort: func() { aborted = true }}
	id, _, err := store.Register(RegisterSpec{
		Type: TypeAgent, Description: "job", AgentType: "explore", Runtime: runtime,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := KillAgent(id, store); err != nil {
		t.Fatalf("KillAgent() error = %v", err)
	}
	if !aborted {
		t.Error("KillAgent() did not call Runtime.Abort")
	}
}

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

func TestSetRuntimeMetadata(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if !store.SetRuntimeMetadata(id, token, &fakeRuntime{outputPath: "/tmp/a.jsonl", worktree: "/tmp/wt"}) {
		t.Fatal("metadata rejected")
	}
	if store.SetRuntimeMetadata(id, token+1, &fakeRuntime{outputPath: "/tmp/b.jsonl"}) {
		t.Fatal("stale token metadata accepted")
	}
	state, _ := store.Task(id)
	if state.Base().OutputFile != "/tmp/a.jsonl" {
		t.Fatalf("output file = %q", state.Base().OutputFile)
	}
	// 空值不能覆盖已写入的路径
	store.SetRuntimeMetadata(id, token, &fakeRuntime{})
	state, _ = store.Task(id)
	if state.Base().OutputFile != "/tmp/a.jsonl" {
		t.Fatalf("empty metadata overwrote output file: %q", state.Base().OutputFile)
	}
}

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

func TestRemoveRuntime(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if _, err := store.AttachAgentRuntime(id, &fakeRuntime{}, "prompt"); err != nil {
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

func TestAttachAgentRuntimeRejectsTerminalTask(t *testing.T) {
	store := NewStore()
	id, token := newAgentTask(t, store, "child")
	if !store.CompleteAgent(id, token, &AgentResult{}) {
		t.Fatal("complete failed")
	}
	if _, err := store.AttachAgentRuntime(id, &fakeRuntime{}, "prompt"); err == nil {
		t.Fatal("terminal task accepted a runtime")
	}
}

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
