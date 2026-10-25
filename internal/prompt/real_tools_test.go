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
		"- bash: Execute bash commands",
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
		"Prefer the find tool for file pattern matching, and the ls tool for listing a directory; use bash `find` or `ls` only when a dedicated tool cannot accomplish the task",
		"Prefer the grep tool for searching file contents; use bash `grep` or `rg` only when a dedicated tool cannot accomplish the task",
		"Prefer the edit tool for editing files; use bash `sed` or `awk` only when a dedicated tool cannot accomplish the task",
		"Prefer the write tool for creating files; use bash redirection or heredocs only when a dedicated tool cannot accomplish the task",
		"Output text directly to communicate; do not use bash `echo` or `printf` for messages to the user",
		"When several independent commands can run in parallel, issue multiple bash tool calls in a single message instead of chaining them",
		"When commands depend on each other, chain them with `&&` in one bash call",
		"Use `;` only when you need to run commands sequentially but do not care whether earlier ones fail",
		"Do not use newlines to separate commands; newlines are fine inside quoted strings",
		"Before creating files or directories, use `ls` to verify the parent directory exists and is the correct location",
		"Always quote file paths that contain spaces with double quotes",
		"Maintain the current working directory by using absolute paths; only use `cd` when the user explicitly asks for it",
		"Prefer creating a new commit over amending an existing one",
		"Before destructive git operations such as `git reset --hard`, `git push --force`, or `git checkout --`, consider whether a safer alternative reaches the same goal; only use them when they are truly the best approach",
		"Never skip hooks with `--no-verify` or bypass signing unless the user has explicitly asked for it; if a hook fails, investigate and fix the underlying issue",
		"Do not sleep between commands that can run immediately; just run them",
		"For long-running commands use `run_in_background` instead of sleeping; you are notified when the task completes, so do not poll",
		"Do not retry failing commands in a sleep loop; diagnose the root cause instead",
		"If you must poll an external process, use a check command rather than sleeping first, and keep any sleep short",
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
