package agenttool

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/tool"
)

func TestAgentToolPromptReachesSystemSections(t *testing.T) {
	sections := prompt.BuildSections(prompt.Options{Cwd: "/repo", Tools: []llm.Tool{New(nil), &tool.Read{}}})
	tools := sections["tools"]
	rules := sections["rules"]
	if !strings.Contains(tools, "subagent") {
		t.Fatalf("tools section missing agent snippet: %q", tools)
	}
	for _, want := range []string{"When to fork", "Writing the prompt", "Never delegate understanding", "When NOT to use the Agent tool"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("rules section missing %q", want)
		}
	}
}

func TestPromptsUseRegisteredToolNames(t *testing.T) {
	// 工具名必须与 <tools> 分节暴露的一致,否则模型会调用不存在的工具
	registered := []string{"bash", "edit", "find", "grep", "ls", "powershell", "read", "write"}
	prompts := map[string]string{
		"explore": explorePrompt(),
		"plan":    planPrompt(),
		"work":    workPrompt,
		"fork":    buildForkDirective("x", "/repo", ""),
	}
	for name, text := range prompts {
		for _, wrong := range []string{"Use Read ", "Use Find ", "Use Grep ", "Use Bash ", "Bash, Read, Write"} {
			if strings.Contains(text, wrong) {
				t.Fatalf("%s prompt uses a capitalized tool name: %q", name, wrong)
			}
		}
		_ = registered
	}
	// Agent tool 的公共规则同样不能出现大写工具名
	for _, guideline := range agentToolGuidelines() {
		for _, wrong := range []string{"use Read instead", "use Grep instead"} {
			if strings.Contains(guideline, wrong) {
				t.Fatalf("agent guideline uses a capitalized tool name: %q", wrong)
			}
		}
	}
}

func TestSubagentTypeAcceptsCustomNames(t *testing.T) {
	schema := New(nil).Parameters()
	properties := schema["properties"].(map[string]any)
	subagentType := properties["subagent_type"].(map[string]any)
	// 不能锁死 enum,否则 .rivo/agent/agents 下的自定义 agent 无法被选中
	if _, ok := subagentType["enum"]; ok {
		t.Fatal("subagent_type must not restrict values to the built-in set")
	}
	if !strings.Contains(subagentType["description"].(string), "Custom agent names") {
		t.Fatal("subagent_type description does not mention custom agents")
	}
}
