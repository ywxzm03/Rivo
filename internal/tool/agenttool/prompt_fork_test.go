package agenttool

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestBuiltinPromptSelectsByAgentType(t *testing.T) {
	cases := map[string]string{
		"explore": "file search specialist",
		"plan":    "software architect",
		"work":    "use the tools available",
	}
	for agentType, want := range cases {
		if got := builtinPrompt(agentType); !containsFold(got, want) {
			t.Fatalf("builtinPrompt(%q) does not contain %q", agentType, want)
		}
	}
}

func TestBuiltinPromptIsCaseInsensitiveAndFallsBack(t *testing.T) {
	// 1.大小写不敏感
	if got := builtinPrompt("EXPLORE"); !containsFold(got, "file search specialist") {
		t.Fatalf("uppercase type fell through: %q", got)
	}
	// 2.未知类型给通用兜底 prompt
	if got := builtinPrompt("custom"); !containsFold(got, "Complete the requested task") {
		t.Fatalf("fallback prompt = %q", got)
	}
}

func TestBuiltinPromptDistinguishesEveryType(t *testing.T) {
	seen := make(map[string]bool)
	for _, agentType := range []string{"explore", "plan", "work", "unknown"} {
		prompt := builtinPrompt(agentType)
		if prompt == "" {
			t.Fatalf("empty prompt for %q", agentType)
		}
		if seen[prompt] {
			t.Fatalf("prompt for %q duplicates another type", agentType)
		}
		seen[prompt] = true
	}
}

func TestIsBuiltinAgent(t *testing.T) {
	for _, name := range []string{"explore", "plan", "work", "Explore", "WORK"} {
		if !isBuiltinAgent(name) {
			t.Fatalf("isBuiltinAgent(%q) = false", name)
		}
	}
	for _, name := range []string{"fork", "custom", "", "general-purpose"} {
		if isBuiltinAgent(name) {
			t.Fatalf("isBuiltinAgent(%q) = true", name)
		}
	}
}

func TestBuildForkMessagesFillsMissingToolResults(t *testing.T) {
	parent := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "go"}}},
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{ID: "tool-1", Name: "read"},
			llm.ToolCall{ID: "tool-2", Name: "grep"},
		}},
		llm.ToolResultMessage{ToolCallID: "tool-1", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "existing"}}},
	}
	child := buildForkMessages(parent, "inspect the result", "", "/repo")
	// 1.每个 tool call 都要有同 ID 的结果:user + assistant + 两个 result + directive
	if len(child) != 5 {
		t.Fatalf("child messages = %d, want 5", len(child))
	}
	first := child[2].(llm.ToolResultMessage)
	second := child[3].(llm.ToolResultMessage)
	if first.ToolCallID != "tool-1" || first.Content[0].(llm.TextContent).Text != "existing" {
		t.Fatalf("existing result was not preserved: %#v", first)
	}
	if second.ToolCallID != "tool-2" || second.Content[0].(llm.TextContent).Text != forkPlaceholder {
		t.Fatalf("missing result was not filled: %#v", second)
	}
	// 2.末尾追加 fork 指令
	directive := child[len(child)-1].(llm.UserMessage).Content[0].(llm.TextContent).Text
	if directive != "Your directive: inspect the result" {
		t.Fatalf("directive = %q", directive)
	}
}

func TestBuildForkMessagesMentionsWorktree(t *testing.T) {
	child := buildForkMessages([]llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "go"}}}}, "work", "/wt", "/repo")
	directive := child[len(child)-1].(llm.UserMessage).Content[0].(llm.TextContent).Text
	// worktree 场景要同时给出 worktree 路径和原 cwd,便于迁移继承路径
	if !containsFold(directive, "/wt") || !containsFold(directive, "/repo") {
		t.Fatalf("worktree directive = %q", directive)
	}
}

func TestBuildForkMessagesDoesNotMutateParent(t *testing.T) {
	parent := []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "tool-1", Name: "read"}}},
	}
	buildForkMessages(parent, "go", "", "/repo")
	if len(parent) != 1 {
		t.Fatalf("parent messages = %d, want 1", len(parent))
	}
	if parent[0].(llm.AssistantMessage).Content[0].(llm.ToolCall).ID != "tool-1" {
		t.Fatal("fork builder mutated parent message identity")
	}
}

func containsFold(haystack, needle string) bool {
	return len(needle) == 0 || indexFold(haystack, needle) >= 0
}

func indexFold(haystack, needle string) int {
	h, n := []rune(haystack), []rune(needle)
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if lower(h[i+j]) != lower(n[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}
