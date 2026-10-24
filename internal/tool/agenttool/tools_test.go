package agenttool

import (
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

func TestReadOnlyToolsKeepInspectionToolsAndReadOnlyBash(t *testing.T) {
	got := readOnlyTools(testParentTools())
	want := []string{"bash", "find", "grep", "ls", "read"}
	if names := toolNames(got); !equalStrings(names, want) {
		t.Fatalf("read-only tools = %v, want %v", names, want)
	}
	// 1.bash 必须降级为只读版本,否则 explore/plan 能写文件
	for _, candidate := range got {
		if candidate.Name() == "bash" {
			if _, ok := candidate.(*toolpkg.ReadOnlyBash); !ok {
				t.Fatalf("bash tool type = %T, want *toolpkg.ReadOnlyBash", candidate)
			}
		}
	}
}

func TestChildToolsKeepWorkToolsAndRemoveTaskStop(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	got := childTools(testParentTools(), true, sm, taskpkg.NewStore())
	want := []string{"agent", "bash", "edit", "find", "grep", "ls", "powershell", "read", "write"}
	if names := toolNames(got); !equalStrings(names, want) {
		t.Fatalf("work tools = %v, want %v", names, want)
	}
}

func TestChildToolsDropsAgentWhenNotKept(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	got := childTools(testParentTools(), false, sm, taskpkg.NewStore())
	// explore/plan 子 agent 不得再派发 subagent
	for _, candidate := range got {
		if candidate.Name() == "agent" {
			t.Fatal("agent tool survived with keepAgent=false")
		}
	}
}

func TestChildToolsRejectsNestedAgentInFork(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	got := childTools(testParentTools(), true, sm, taskpkg.NewStore())
	var nested llm.Tool
	for _, candidate := range got {
		if candidate.Name() == "agent" {
			nested = candidate
		}
	}
	if nested == nil {
		t.Fatal("agent tool missing in fork child")
	}
	result, err := nested.Execute(nil, map[string]any{"description": "d", "prompt": "p"})
	if err != nil {
		t.Fatal(err)
	}
	// 1.嵌套调用返回错误结果而不是 panic 或静默成功
	if !result.IsError {
		t.Fatalf("nested agent call = %#v, want error result", result)
	}
	if !containsFold(result.Content[0].(llm.TextContent).Text, "nested subagents") {
		t.Fatalf("nested error text = %q", result.Content[0].(llm.TextContent).Text)
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
