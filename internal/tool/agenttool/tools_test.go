package agenttool

import (
	"context"
	"sort"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	toolpkg "github.com/ywxzm03/Rivo/internal/tool"
	"github.com/ywxzm03/Rivo/internal/tool/taskstoptool"
)

func testParentTools() []llm.Tool {
	return []llm.Tool{
		&toolpkg.Read{}, &toolpkg.Ls{}, &toolpkg.Grep{}, &toolpkg.Find{}, &toolpkg.Bash{},
		&toolpkg.Write{}, &toolpkg.Edit{}, New(nil), taskstoptool.New(taskpkg.NewStore()), &toolpkg.PowerShell{},
	}
}

func toolNames(tools []llm.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, candidate := range tools {
		names = append(names, candidate.Name())
	}
	sort.Strings(names)
	return names
}

func TestReadOnlyToolsKeepInspectionToolsAndBash(t *testing.T) {
	got := readOnlyTools(testParentTools())
	// TaskStop 必须保留:explore 能用 bash 起后台任务,就必须能停止它
	want := []string{"TaskStop", "bash", "find", "grep", "ls", "read"}
	if names := toolNames(got); !equalStrings(names, want) {
		t.Fatalf("read-only tools = %v, want %v", names, want)
	}
	// bash 保持完整能力,只读性由工具池裁剪与 prompt 约束保证
	for _, candidate := range got {
		if candidate.Name() == "bash" {
			if _, ok := candidate.(*toolpkg.Bash); !ok {
				t.Fatalf("bash tool type = %T, want *toolpkg.Bash", candidate)
			}
		}
	}
}

func TestChildToolsKeepTaskStopForEveryChild(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	for _, fork := range []bool{true, false} {
		got := childTools(testParentTools(), fork, sm, taskpkg.NewStore())
		// TaskStop 对所有子 agent 可见,便于终止自己启动的后台任务
		found := false
		for _, candidate := range got {
			if candidate.Name() == "TaskStop" {
				found = true
			}
		}
		if !found {
			t.Fatalf("fork=%v: TaskStop missing from child tools", fork)
		}
	}
}

func TestChildToolsDropsAgentForTypedSubagent(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	got := childTools(testParentTools(), false, sm, taskpkg.NewStore())
	// explore/plan/work 子 agent 不得再派发 subagent
	for _, candidate := range got {
		if candidate.Name() == "agent" {
			t.Fatal("agent tool survived for a typed subagent")
		}
	}
}

func TestChildToolsReusesParentAgentForFork(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	parent := testParentTools()
	var parentAgent llm.Tool
	for _, candidate := range parent {
		if candidate.Name() == "agent" {
			parentAgent = candidate
		}
	}
	got := childTools(parent, true, sm, taskpkg.NewStore())
	// 1.fork 必须拿到父的原始实例,而不是包装类型
	for _, candidate := range got {
		if candidate.Name() == "agent" {
			if candidate != parentAgent {
				t.Fatalf("fork agent tool = %T, want the parent instance", candidate)
			}
			return
		}
	}
	t.Fatal("agent tool missing in fork child")
}

func TestChildToolsRebindsWriteAndEdit(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	parent := testParentTools()
	var parentWrite, parentEdit llm.Tool
	for _, candidate := range parent {
		switch candidate.Name() {
		case "write":
			parentWrite = candidate
		case "edit":
			parentEdit = candidate
		}
	}
	got := childTools(parent, true, sm, taskpkg.NewStore())
	for _, candidate := range got {
		switch candidate.Name() {
		case "write":
			// 2.写类工具必须换实例,避免子 agent 的改动进入父会话的 undo 快照
			if candidate == parentWrite {
				t.Fatal("write tool was not rebound to the child session")
			}
		case "edit":
			if candidate == parentEdit {
				t.Fatal("edit tool was not rebound to the child session")
			}
		}
	}
}

func TestFilterAgentTools(t *testing.T) {
	parent := testParentTools()
	// 1.空列表表示不过滤
	if got, err := filterAgentTools(parent, nil); err != nil || len(got) != len(parent) {
		t.Fatalf("empty filter = %d tools, %v", len(got), err)
	}
	// 2.通配符表示全部保留
	if got, err := filterAgentTools(parent, []string{"*"}); err != nil || len(got) != len(parent) {
		t.Fatalf("wildcard filter = %d tools, %v", len(got), err)
	}
	// 3.显式列表只保留匹配项,大小写不敏感
	got, err := filterAgentTools(parent, []string{"Read", "grep"})
	if err != nil {
		t.Fatal(err)
	}
	if names := toolNames(got); !equalStrings(names, []string{"grep", "read"}) {
		t.Fatalf("filtered tools = %v", names)
	}
	// 4.请求不存在的工具必须报错,而不是静默少给
	if _, err := filterAgentTools(parent, []string{"nonexistent"}); err == nil {
		t.Fatal("unknown tool name was accepted")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// fork 子 agent 保留 Agent 工具以便工具定义与父缓存一致
func TestForkChildKeepsAgentTool(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	store := taskpkg.NewStore()
	got := childTools(testParentTools(), true, sm, store)
	found := false
	for _, candidate := range got {
		if candidate.Name() == "agent" {
			found = true
		}
	}
	if !found {
		t.Fatal("fork child lost the agent tool")
	}
	// typed subagent 不能继续派发
	typed := childTools(testParentTools(), false, sm, store)
	for _, candidate := range typed {
		if candidate.Name() == "agent" {
			t.Fatal("typed subagent kept the agent tool")
		}
	}
}

// fork 来源标记必须从 context 传入 AgentInput
func TestAgentToolPropagatesForkChild(t *testing.T) {
	launcher := &recordingLauncher{}
	tool := New(launcher)
	ctx := llm.WithToolContext(context.Background(), llm.ToolContext{ForkChild: true})
	_, _ = tool.Execute(ctx, map[string]any{"description": "d", "prompt": "p"})
	if !launcher.input.ForkChild {
		t.Fatal("fork child flag was not propagated to the launcher")
	}
}

type recordingLauncher struct{ input taskpkg.AgentInput }

func (l *recordingLauncher) Start(_ context.Context, input taskpkg.AgentInput) (taskpkg.AgentLaunched, error) {
	l.input = input
	return taskpkg.AgentLaunched{AgentID: "agent-1"}, nil
}

// thinking 等级必须校验,避免非法值透传给 provider
func TestParseInputRejectsUnknownThinking(t *testing.T) {
	if _, err := parseInput(map[string]any{"description": "d", "prompt": "p", "thinking": "medum"}); err == nil {
		t.Fatal("invalid thinking level was accepted")
	}
	if _, err := parseInput(map[string]any{"description": "d", "prompt": "p", "thinking": "high"}); err != nil {
		t.Fatalf("valid thinking level was rejected: %v", err)
	}
}
