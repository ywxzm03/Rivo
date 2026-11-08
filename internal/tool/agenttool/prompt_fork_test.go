package agenttool

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

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
	// 2.末尾追加 fork 指令,含 boilerplate 前缀与 directive 正文
	directive := child[len(child)-1].(llm.UserMessage).Content[0].(llm.TextContent).Text
	if !containsFold(directive, "<fork-boilerplate>") {
		t.Fatalf("fork boilerplate missing: %q", directive)
	}
	if !containsFold(directive, "Your directive: inspect the result") {
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

func TestForkDirectiveCarriesBoilerplateAndNoNestedAgent(t *testing.T) {
	directive := buildForkDirective("audit the branch", "/repo", "")
	// 1.boilerplate 必须在 directive 之前
	if !strings.HasPrefix(directive, "<fork-boilerplate>") {
		t.Fatalf("boilerplate is not the prefix: %q", directive)
	}
	if !strings.Contains(directive, "</fork-boilerplate>") {
		t.Fatal("boilerplate is not closed")
	}
	// 2.关键行为约束必须保留
	for _, rule := range []string{"Do NOT converse", "Do NOT editorialize", "Scope:", "under 500 words"} {
		if !strings.Contains(directive, rule) {
			t.Fatalf("rule %q missing from boilerplate", rule)
		}
	}
	// 3.禁止嵌套调用 Agent 的提醒放在最后
	if !strings.HasSuffix(strings.TrimSpace(directive), forkNoNestedAgent) {
		t.Fatalf("nested-agent reminder is not last: %q", directive)
	}
	// 4.没有 worktree 时不出现隔离说明
	if strings.Contains(directive, "isolated git worktree") {
		t.Fatal("worktree notice leaked without a worktree")
	}
}

func TestForkDirectiveIncludesWorktreeNotice(t *testing.T) {
	directive := buildForkDirective("work", "/repo", "/wt")
	// worktree 场景要同时给出父 cwd 与 worktree 路径,并声明改动隔离
	for _, want := range []string{"/repo", "/wt", "isolated git worktree", "will not affect the parent's files"} {
		if !strings.Contains(directive, want) {
			t.Fatalf("worktree notice missing %q", want)
		}
	}
	// notice 必须排在 directive 正文之前
	if strings.Index(directive, "isolated git worktree") > strings.Index(directive, "Your directive:") {
		t.Fatal("worktree notice should precede the directive")
	}
}

// fork 前缀共享父消息引用,尾部深拷贝:子 slice 的改写不得回写父 transcript
func TestBuildForkMessagesSharesPrefixAndIsolatesTail(t *testing.T) {
	parent := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "parent prompt"}}},
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{ID: "tool-1", Name: "read"},
			llm.ToolCall{ID: "tool-2", Name: "grep"},
		}},
		llm.ToolResultMessage{ToolCallID: "tool-1", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "existing"}}},
	}
	child := buildForkMessages(parent, "go", "", "/repo")
	// 1.前缀(最后一条 assistant 之前)共享同一份 Content 底层数组
	parentText := parent[0].(llm.UserMessage).Content
	childText := child[0].(llm.UserMessage).Content
	if len(parentText) != 1 || len(childText) != 1 {
		t.Fatalf("prefix content = %d/%d, want 1/1", len(parentText), len(childText))
	}
	if &parentText[0] != &childText[0] {
		t.Fatal("fork prefix was deep copied instead of shared")
	}
	// 2.子 slice 改写不影响父 slice
	child[0] = llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "rewritten"}}}
	child[2] = llm.ToolResultMessage{ToolCallID: "tool-1", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "rewritten"}}}
	if parent[0].(llm.UserMessage).Content[0].(llm.TextContent).Text != "parent prompt" {
		t.Fatal("child slice rewrite leaked into the parent prefix")
	}
	if parent[2].(llm.ToolResultMessage).Content[0].(llm.TextContent).Text != "existing" {
		t.Fatal("child slice rewrite leaked into the parent tail")
	}
	// 3.子 agent 的 append 不得写进父的 backing array
	capacity := cap(child)
	parentLen := len(parent)
	child = append(child, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "extra"}}})
	if cap(child) == capacity {
		t.Fatal("fork slice has spare capacity: appends could alias the parent array")
	}
	if len(parent) != parentLen {
		t.Fatalf("parent length changed to %d, want %d", len(parent), parentLen)
	}
}

// 尾部 tool result 的占位替换只发生在深拷贝上,父的原有顺序保持原样
func TestBuildForkMessagesDoesNotReorderParentTail(t *testing.T) {
	parent := []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{ID: "tool-1", Name: "read"},
			llm.ToolCall{ID: "tool-2", Name: "grep"},
		}},
		llm.ToolResultMessage{ToolCallID: "tool-1", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "existing"}}},
	}
	buildForkMessages(parent, "go", "", "/repo")
	if len(parent) != 2 {
		t.Fatalf("parent tail length = %d, want 2", len(parent))
	}
	if parent[1].(llm.ToolResultMessage).ToolCallID != "tool-1" {
		t.Fatalf("parent tail was reordered: %#v", parent[1])
	}
}
