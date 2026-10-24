package app

import (
	"sort"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/subagent"
	"github.com/ywxzm03/Rivo/internal/tool"
)

func testParentTools() []llm.Tool {
	manager := subagent.NewManager(subagent.NewStore(), nil)
	return []llm.Tool{
		&tool.Read{}, &tool.Ls{}, &tool.Grep{}, &tool.Find{}, &tool.Bash{},
		&tool.Write{}, &tool.Edit{}, manager.AgentTool(), manager.TaskStopTool(), &tool.PowerShell{},
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
	for _, candidate := range got {
		if candidate.Name() == "bash" {
			if _, ok := candidate.(*tool.ReadOnlyBash); !ok {
				t.Fatalf("bash tool type = %T, want *tool.ReadOnlyBash", candidate)
			}
		}
	}
}

func TestChildToolsKeepWorkToolsAndRemoveTaskStop(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	got := childTools(testParentTools(), true, sm, subagent.NewStore())
	want := []string{"agent", "bash", "edit", "find", "grep", "ls", "powershell", "read", "write"}
	if names := toolNames(got); !equalStrings(names, want) {
		t.Fatalf("work tools = %v, want %v", names, want)
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

func TestBuildForkMessagesCreatesToolResultPlaceholders(t *testing.T) {
	parent := []llm.Message{
		llm.SystemMessage{Sections: map[string]string{"preamble": "parent"}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "request"}}},
		llm.AssistantMessage{Content: []llm.Content{
			llm.TextContent{Text: "starting"},
			llm.ToolCall{ID: "tool-1", Name: "bash", Arguments: map[string]any{"command": "pwd"}},
			llm.ToolCall{ID: "tool-2", Name: "read", Arguments: map[string]any{"path": "main.go"}},
		}},
	}
	child := buildForkMessages(parent, "inspect the result", "", "/repo")
	if len(child) != 6 {
		t.Fatalf("fork messages = %d, want system/user/assistant/two results/directive", len(child))
	}
	for i, want := range []string{"tool-1", "tool-2"} {
		result, ok := child[3+i].(llm.ToolResultMessage)
		if !ok || result.ToolCallID != want {
			t.Fatalf("placeholder %d = %#v, want tool result for %s", i, child[3+i], want)
		}
	}
	directive := child[len(child)-1].(llm.UserMessage).Content[0].(llm.TextContent).Text
	if directive != "Your directive: inspect the result" {
		t.Fatalf("directive = %q", directive)
	}
	if parent[len(parent)-1].(llm.AssistantMessage).Content[1].(llm.ToolCall).ID != "tool-1" {
		t.Fatal("fork builder mutated parent message identity")
	}
}

func TestBuildForkMessagesOrdersExistingAndMissingToolResults(t *testing.T) {
	parent := []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{ID: "call-1", Name: "read"},
			llm.ToolCall{ID: "call-2", Name: "grep"},
		}},
		llm.ToolResultMessage{ToolCallID: "call-1", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "ok"}}},
	}
	child := buildForkMessages(parent, "continue", "", "/repo")
	first := child[1].(llm.ToolResultMessage)
	second := child[2].(llm.ToolResultMessage)
	if first.ToolCallID != "call-1" || second.ToolCallID != "call-2" {
		t.Fatalf("tool result order = %q, %q", first.ToolCallID, second.ToolCallID)
	}
}
