package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tui"
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

// 弹窗需要的运行时字段必须与 store 状态一致
func TestShellListProjectsRuntimeFields(t *testing.T) {
	r := &Runtime{}
	store := shellModelStore(t)
	shells := r.shellViews(store.Snapshot())
	if len(shells) != 1 {
		t.Fatalf("shells = %d, want 1", len(shells))
	}
	shell := shells[0]
	if shell.ID == "" {
		t.Fatal("ID empty, want the store task id")
	}
	if shell.StartTime.IsZero() {
		t.Fatal("StartTime zero, want the registration time")
	}
	if shell.EndTime != nil {
		t.Fatalf("EndTime = %v, want nil while running", shell.EndTime)
	}
	if shell.ExitCode != nil {
		t.Fatalf("ExitCode = %v, want nil while running", shell.ExitCode)
	}
	if shell.OutputBytes <= 0 {
		t.Fatalf("OutputBytes = %d, want the file size", shell.OutputBytes)
	}
}

// task.KillBash 只终止目标任务,不影响同 store 里的其他 shell
func TestKillBashStopsOnlyTarget(t *testing.T) {
	store := task.NewStore()
	first, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "first", BashState: &task.BashState{Command: "first"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "second", BashState: &task.BashState{Command: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := task.KillBash(first, store); err != nil {
		t.Fatalf("KillBash = %v", err)
	}
	firstState, _ := store.Task(first)
	secondState, _ := store.Task(second)
	if firstState.Base().Status == task.StatusRunning {
		t.Fatal("killed shell is still running")
	}
	if secondState.Base().Status != task.StatusRunning {
		t.Fatalf("second shell status = %s, want running", secondState.Base().Status)
	}
}

// 快照来自 map 遍历,投影必须按启动时间稳定排序
func TestShellViewsSortsByStartTime(t *testing.T) {
	store := task.NewStore()
	first, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "first", BashState: &task.BashState{Command: "first"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "second", BashState: &task.BashState{Command: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{}
	for range 5 {
		shells := r.shellViews(store.Snapshot())
		if len(shells) != 2 {
			t.Fatalf("shells = %d, want 2", len(shells))
		}
		if shells[0].ID != first || shells[1].ID != second {
			t.Fatalf("order = %s,%s, want %s,%s", shells[0].ID, shells[1].ID, first, second)
		}
	}
}

// ShellKill 把终止结果包成 ShellKilledMsg,并真的停掉目标 shell
func TestShellKillStopsSingleShell(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}
	activeApp, err := app.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, activeApp.Cwd(), "")
	r.Attach(activeApp)

	store := activeApp.SubagentStore()
	if store == nil {
		t.Fatal("subagent store is unavailable")
	}
	target, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "target", BashState: &task.BashState{Command: "target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "other", BashState: &task.BashState{Command: "other"},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, ok := r.ShellKill(target)().(tui.ShellKilledMsg)
	if !ok {
		t.Fatal("ShellKill did not return a ShellKilledMsg")
	}
	if msg.ID != target || msg.Err != nil {
		t.Fatalf("msg = %+v, want the target id without error", msg)
	}
	targetState, _ := store.Task(target)
	otherState, _ := store.Task(other)
	if targetState.Base().Status == task.StatusRunning {
		t.Fatal("target shell is still running")
	}
	if otherState.Base().Status != task.StatusRunning {
		t.Fatalf("other shell status = %s, want running", otherState.Base().Status)
	}
}

// /stop 只停后台 shell,绝不能动子 agent
func TestShellStopAllDoesNotStopSubagents(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}
	activeApp, err := app.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, activeApp.Cwd(), "")
	r.Attach(activeApp)

	store := activeApp.SubagentStore()
	if store == nil {
		t.Fatal("subagent store is unavailable")
	}
	agentID, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeAgent, Description: "work",
		AgentState: &task.AgentState{AgentID: "a1", AgentType: "Work", Prompt: "go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bashID, _, err := store.Register(task.RegisterSpec{
		Type: task.TypeBash, Description: "sleep 120", BashState: &task.BashState{Command: "sleep 120"},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, ok := r.ShellStopAll()().(tui.ShellsStoppedMsg)
	if !ok {
		t.Fatal("ShellStopAll did not return a ShellsStoppedMsg")
	}
	if msg.Err != nil {
		t.Fatalf("Err = %v, want nil", msg.Err)
	}
	if msg.Stopped != 1 {
		t.Fatalf("Stopped = %d, want 1", msg.Stopped)
	}
	agent, _ := store.Task(agentID)
	if agent.Base().Status != task.StatusRunning {
		t.Fatalf("agent status = %s, want running", agent.Base().Status)
	}
	bash, _ := store.Task(bashID)
	if bash.Base().Status == task.StatusRunning {
		t.Fatal("bash still running")
	}
}

// 没有活动 App 时 ShellKill 返回不可用错误而不是 panic
func TestShellKillWithoutAppReportsUnavailable(t *testing.T) {
	r := &Runtime{}
	msg, ok := r.ShellKill("bash-1")().(tui.ShellKilledMsg)
	if !ok {
		t.Fatal("ShellKill did not return a ShellKilledMsg")
	}
	if msg.Err == nil {
		t.Fatal("Err nil, want the unavailable error")
	}
}
