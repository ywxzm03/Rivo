package prompt

import (
	"context"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// stubTool 用于分节构建的工具桩
type stubTool struct {
	name       string
	snippet    string
	guidelines []string
}

func (s stubTool) Name() string               { return s.name }
func (s stubTool) Description() string        { return "" }
func (s stubTool) Parameters() map[string]any { return nil }
func (s stubTool) PromptSnippet() string      { return s.snippet }
func (s stubTool) PromptGuidelines() []string { return s.guidelines }
func (s stubTool) ExecutionMode() llm.ToolExecutionMode {
	return llm.ToolExecutionParallel
}
func (s stubTool) Execute(_ context.Context, _ map[string]any) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func testTools() []llm.Tool {
	return []llm.Tool{
		stubTool{name: "read", snippet: "Read file contents", guidelines: []string{"Use read to examine files instead of cat or sed."}},
		stubTool{name: "ls", snippet: "List directory contents"},
		stubTool{name: "write", snippet: "Create or overwrite files", guidelines: []string{"Use write only for new files or complete rewrites."}},
	}
}

func TestBuildSectionsRendersAllSections(t *testing.T) {
	sections := BuildSections(Options{Cwd: "/work"})

	if sections["preamble"] == "" {
		t.Fatal("preamble missing")
	}
	// preamble 不带标签，其余节包在 <节名> 里
	if strings.Contains(sections["preamble"], "<preamble>") {
		t.Errorf("preamble = %q, want untagged text", sections["preamble"])
	}
	if !strings.HasPrefix(sections["tools"], "<tools>\n") || !strings.HasSuffix(sections["tools"], "\n</tools>") {
		t.Errorf("tools = %q, want <tools> wrapper", sections["tools"])
	}
	if !strings.HasPrefix(sections["rules"], "<rules>\n") || !strings.HasSuffix(sections["rules"], "\n</rules>") {
		t.Errorf("rules = %q, want <rules> wrapper", sections["rules"])
	}
	if sections["cwd"] != "/work" {
		t.Errorf("cwd = %q, want /work", sections["cwd"])
	}
}

func TestBuildSectionsOmitsEmptyOptionalSections(t *testing.T) {
	sections := BuildSections(Options{Cwd: "/work"})
	if _, ok := sections["instructions"]; ok {
		t.Error("instructions present without context files")
	}
	if _, ok := sections["skills"]; ok {
		t.Error("skills present without skills")
	}
}

func TestBuildSectionsToolsListsSnippetsInOrder(t *testing.T) {
	sections := BuildSections(Options{Cwd: "/work", Tools: testTools()})
	want := "<tools>\n- read: Read file contents\n- ls: List directory contents\n- write: Create or overwrite files\n</tools>"
	if sections["tools"] != want {
		t.Errorf("tools =\n%q\nwant\n%q", sections["tools"], want)
	}
	// 不追加 PI 的 custom tools 尾句
	if strings.Contains(sections["tools"], "custom tools") {
		t.Errorf("tools = %q, want no custom-tools sentence", sections["tools"])
	}
}

func TestBuildSectionsToolsSkipsEmptySnippet(t *testing.T) {
	tools := []llm.Tool{stubTool{name: "read", snippet: "Read file contents"}, stubTool{name: "hidden"}}
	sections := BuildSections(Options{Cwd: "/work", Tools: tools})
	if strings.Contains(sections["tools"], "hidden") {
		t.Errorf("tools = %q, want tool without snippet omitted", sections["tools"])
	}
}

func TestBuildSectionsRulesOrderAndDedup(t *testing.T) {
	tools := []llm.Tool{
		stubTool{name: "read", guidelines: []string{"shared", " only-read "}},
		stubTool{name: "ls", guidelines: []string{"shared", ""}},
	}
	sections := BuildSections(Options{Cwd: "/work", Tools: tools})
	want := "<rules>\n- shared\n- only-read\n- Be concise in your responses\n- Show file paths clearly when working with files\n</rules>"
	if sections["rules"] != want {
		t.Errorf("rules =\n%q\nwant\n%q", sections["rules"], want)
	}
}

func TestBuildSectionsInstructionsRendering(t *testing.T) {
	files := []ContextFile{
		{Path: "/a/AGENTS.md", Content: "global"},
		{Path: "/b/AGENTS.md", Content: "project"},
	}
	sections := BuildSections(Options{Cwd: "/work", ContextFiles: files})
	want := "<instructions>\nProject-specific instructions and guidelines:\n\n" +
		"<instructions path=\"/a/AGENTS.md\">\nglobal\n</instructions>\n\n" +
		"<instructions path=\"/b/AGENTS.md\">\nproject\n</instructions>\n</instructions>"
	if sections["instructions"] != want {
		t.Errorf("instructions =\n%q\nwant\n%q", sections["instructions"], want)
	}
}

func TestBuildSectionsCwdNormalizesBackslashes(t *testing.T) {
	sections := BuildSections(Options{Cwd: `C:\Users\me\proj`})
	if sections["cwd"] != "C:/Users/me/proj" {
		t.Errorf("cwd = %q, want forward slashes", sections["cwd"])
	}
}

func TestBuildSectionsSkillsRendering(t *testing.T) {
	skills := []Skill{
		{Name: "one", Description: "first", FilePath: "/s/one/SKILL.md"},
		{Name: "hidden", Description: "secret", FilePath: "/s/hidden/SKILL.md", DisableModelInvocation: true},
	}
	sections := BuildSections(Options{Cwd: "/work", Skills: skills})
	text := sections["skills"]
	if !strings.Contains(text, "<name>one</name>") || !strings.Contains(text, "<location>/s/one/SKILL.md</location>") {
		t.Errorf("skills = %q, want visible skill listed", text)
	}
	// disable-model-invocation 的 skill 不出现
	if strings.Contains(text, "hidden") {
		t.Errorf("skills = %q, want hidden skill omitted", text)
	}
}

func TestBuildSectionsSkillsEscapesXML(t *testing.T) {
	skills := []Skill{{Name: "one", Description: "a <b> & \"c\"", FilePath: "/s/one/SKILL.md"}}
	sections := BuildSections(Options{Cwd: "/work", Skills: skills})
	if !strings.Contains(sections["skills"], "a &lt;b&gt; &amp; &quot;c&quot;") {
		t.Errorf("skills = %q, want escaped description", sections["skills"])
	}
}

func TestDiffSectionsReturnsOnlyChanges(t *testing.T) {
	current := Sections{"preamble": "p", "cwd": "/old"}
	target := Sections{"preamble": "p", "cwd": "/new"}
	patch := DiffSections(current, target)
	if len(patch) != 1 || patch["cwd"] != "/new" {
		t.Errorf("patch = %v, want only cwd change", patch)
	}
}

func TestDiffSectionsMarksRemovedSectionEmpty(t *testing.T) {
	current := Sections{"preamble": "p", "instructions": "<instructions>x</instructions>"}
	target := Sections{"preamble": "p"}
	patch := DiffSections(current, target)
	if len(patch) != 1 || patch["instructions"] != "" {
		t.Errorf("patch = %v, want instructions removal", patch)
	}
}

func TestDiffSectionsReturnsNilWhenEqual(t *testing.T) {
	sections := Sections{"preamble": "p", "cwd": "/a"}
	if patch := DiffSections(sections, Sections{"preamble": "p", "cwd": "/a"}); patch != nil {
		t.Errorf("patch = %v, want nil", patch)
	}
}
