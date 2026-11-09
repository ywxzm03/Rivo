package agenttool

import (
	"os"
	"path/filepath"
	"testing"
)

// 自定义 agent 先查项目级 .rivo/agents，再查用户目录
func TestLoadDefinitionPrefersProjectDir(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(agentDir, "agents", "custom.md"), "---\ndescription: user\n---\nuser body")
	write(filepath.Join(cwd, ".rivo", "agents", "custom.md"), "---\ndescription: project\n---\nproject body")

	definition, err := LoadDefinition(cwd, agentDir, "custom")
	if err != nil {
		t.Fatalf("LoadDefinition() error = %v", err)
	}
	if definition.Description != "project" || definition.Prompt != "project body" {
		t.Fatalf("definition = %+v, want the project-level file", definition)
	}
}

// agent 定义里的 thinking 必须是受支持的值,拼错应在加载期报错
func TestParseDefinitionRejectsUnknownThinking(t *testing.T) {
	if _, err := parseDefinition("---\nthinking: medum\n---\nbody", "custom"); err == nil {
		t.Fatal("invalid thinking level was accepted")
	}
	definition, err := parseDefinition("---\nthinking: high\n---\nbody", "custom")
	if err != nil {
		t.Fatalf("valid thinking level was rejected: %v", err)
	}
	if definition.Thinking != "high" {
		t.Fatalf("thinking = %q, want high", definition.Thinking)
	}
}
