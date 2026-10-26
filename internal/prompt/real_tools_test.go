package prompt

import (
	"runtime"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tool"
)

// realTools 按装配顺序返回真实工具，用于锁定模型实际看到的文案
func realTools() []llm.Tool {
	tools := []llm.Tool{
		&tool.Read{},
		&tool.Ls{},
		&tool.Grep{},
		&tool.Find{},
		&tool.Bash{},
		&tool.Write{},
		&tool.Edit{},
	}
	if runtime.GOOS == "windows" {
		tools = append(tools, &tool.PowerShell{})
	}
	return tools
}

// TestRealToolsPromptSnippets 锁定每个真实工具的 snippet 文案与出现顺序
func TestRealToolsPromptSnippets(t *testing.T) {
	sections := BuildSections(Options{Cwd: "/work", Tools: realTools()})
	lines := strings.Split(sections["tools"], "\n")
	var got []string
	for _, line := range lines {
		if strings.HasPrefix(line, "- ") {
			got = append(got, line)
		}
	}
	want := []string{
		"- read: Read file contents",
		"- ls: List directory contents",
		"- grep: Search file contents for patterns (respects .gitignore)",
		"- find: Find files by glob pattern (respects .gitignore)",
		"- bash: Execute bash commands (ls, grep, find, etc.)",
		"- write: Create or overwrite files",
		"- edit: Make precise file edits with exact text replacement, including multiple disjoint edits in one call",
	}
	if runtime.GOOS == "windows" {
		want = append(want, "- powershell: Execute PowerShell commands")
	}
	if len(got) != len(want) {
		t.Fatalf("snippet lines = %d, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRealToolsPromptGuidelines 锁定 guideline 文案、顺序与「哪些工具没有 guideline」
func TestRealToolsPromptGuidelines(t *testing.T) {
	sections := BuildSections(Options{Cwd: "/work", Tools: realTools()})
	rules := sections["rules"]
	want := []string{
		"Use read to examine files instead of cat or sed.",
		"Use write only for new files or complete rewrites.",
		"Use edit for precise changes (edits[].oldText must match exactly)",
		"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
		"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
		"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
		"Be concise in your responses",
		"Show file paths clearly when working with files",
	}
	var got []string
	for _, line := range strings.Split(rules, "\n") {
		if strings.HasPrefix(line, "- ") {
			got = append(got, strings.TrimPrefix(line, "- "))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("rules = %d, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rule %d = %q, want %q", i, got[i], want[i])
		}
	}
	// ls / grep / find / bash / powershell 不贡献 guideline，环境变量提示不得出现
	for _, absent := range []string{"PowerShell to", "environment variables"} {
		if strings.Contains(rules, absent) {
			t.Errorf("rules contains %q, want tool without guidelines to contribute nothing", absent)
		}
	}
}

// TestBuildInstructionsEscapesPath instructions 的 path 属性需转义，避免破坏标签结构
func TestBuildInstructionsEscapesPath(t *testing.T) {
	files := []ContextFile{{Path: `/a/"quoted"<tag>/AGENTS.md`, Content: "body"}}
	sections := BuildSections(Options{Cwd: "/work", ContextFiles: files})
	if strings.Contains(sections["instructions"], `path="/a/"quoted"`) {
		t.Errorf("instructions = %q, want path escaped", sections["instructions"])
	}
	if !strings.Contains(sections["instructions"], "&quot;quoted&quot;&lt;tag&gt;") {
		t.Errorf("instructions = %q, want escaped path attributes", sections["instructions"])
	}
}
