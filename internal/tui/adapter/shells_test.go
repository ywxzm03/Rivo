package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/task"
)

// shellModelStore 构造一个同时含 agent 与 bash 的 store
func shellModelStore(t *testing.T) *task.Store {
	t.Helper()
	store := task.NewStore()
	outputPath := filepath.Join(t.TempDir(), "bash.out")
	if err := os.WriteFile(outputPath, []byte("line one\nline two\nline three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeAgent, Description: "write gin demo",
		AgentState: &task.AgentState{AgentID: "a1", AgentType: "Work", Prompt: "go"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "sleep 120", OutputFile: outputPath,
		BashState: &task.BashState{Command: "sleep 120"},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

// 任务列表只含子 agent，bash 只贡献计数
func TestProjectTasksExcludesBash(t *testing.T) {
	r := &Runtime{}
	store := shellModelStore(t)
	snapshot := r.projectSubagentSnapshot(store, store.Snapshot(), "")

	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Description != "write gin demo" {
		t.Fatalf("tasks = %#v, want only the agent task", snapshot.Tasks)
	}
	if snapshot.RunningShells != 1 {
		t.Fatalf("RunningShells = %d, want 1", snapshot.RunningShells)
	}
}

// bash 结束后不再计入运行中的 shell
func TestProjectRunningShellsCountsOnlyRunning(t *testing.T) {
	r := &Runtime{}
	store := shellModelStore(t)
	store.KillAllRunningTasks()
	snapshot := r.projectSubagentSnapshot(store, store.Snapshot(), "")
	if snapshot.RunningShells != 0 {
		t.Fatalf("RunningShells = %d, want 0", snapshot.RunningShells)
	}
}

// /ps 读取输出文件末尾若干行
func TestShellListReadsOutput(t *testing.T) {
	r := &Runtime{}
	store := shellModelStore(t)
	shells := r.shellViews(store.Snapshot())

	if len(shells) != 1 {
		t.Fatalf("shells = %d, want 1", len(shells))
	}
	if shells[0].Command != "sleep 120" {
		t.Fatalf("command = %q, want %q", shells[0].Command, "sleep 120")
	}
	joined := strings.Join(shells[0].OutputTail, "\n")
	if !strings.Contains(joined, "line three") {
		t.Fatalf("output tail = %q, want it to contain the last line", joined)
	}
	if shells[0].OutputError != "" {
		t.Fatalf("OutputError = %q, want empty", shells[0].OutputError)
	}
}

// 输出文件不存在时记录错误而不是整块失败
func TestShellListMissingOutputFile(t *testing.T) {
	store := task.NewStore()
	if _, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "gone", OutputFile: filepath.Join(t.TempDir(), "missing.out"),
		BashState: &task.BashState{Command: "gone"},
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{}
	shells := r.shellViews(store.Snapshot())
	if len(shells) != 1 {
		t.Fatalf("shells = %d, want 1", len(shells))
	}
	if shells[0].OutputError == "" {
		t.Fatal("OutputError empty, want the read failure recorded")
	}
}
