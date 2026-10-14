package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

// setupPromptEnv 把 HOME 与 agent 目录指向临时目录，隔离磁盘资源
func setupPromptEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	agentDir := filepath.Join(home, "agent")
	t.Setenv("RIVO_AGENT_DIR", agentDir)
	t.Setenv("OPENAI_API_KEY", "test-key")
	return agentDir
}

func writePromptFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestNewLoadsAgentsMdIntoBaseline 装配时把 AGENTS.md 读进基线 instructions 分节
func TestNewLoadsAgentsMdIntoBaseline(t *testing.T) {
	setupPromptEnv(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	writePromptFile(t, filepath.Join(cwd, "AGENTS.md"), "project instructions")
	t.Cleanup(func() { _ = os.Remove(filepath.Join(cwd, "AGENTS.md")) })

	a, err := New(configWithBuiltin(openai.ProviderID, openai.DefaultModel), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if len(a.loader.ContextFiles()) == 0 {
		t.Fatal("loader context files empty, want AGENTS.md loaded")
	}
	sections := a.coordinator.PromptSections()
	if sections["instructions"] == "" {
		t.Errorf("sections = %v, want instructions present", sections)
	}
}

// TestReloadPicksUpChangedAgentsMd Reload 重读磁盘并更新基线分节
func TestReloadPicksUpChangedAgentsMd(t *testing.T) {
	setupPromptEnv(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	writePromptFile(t, agentsPath, "first version")
	t.Cleanup(func() { _ = os.Remove(agentsPath) })

	a, err := New(configWithBuiltin(openai.ProviderID, openai.DefaultModel), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	before := a.coordinator.PromptSections()["instructions"]
	if before == "" {
		t.Fatal("instructions missing before reload")
	}

	writePromptFile(t, agentsPath, "second version")
	a.Reload()

	after := a.coordinator.PromptSections()["instructions"]
	if after == before {
		t.Fatal("instructions unchanged after Reload, want refreshed content")
	}
	if !strings.Contains(after, "second version") {
		t.Errorf("instructions = %q, want new content", after)
	}
}

// TestReloadPicksUpSkills Reload 同时重读 skills
func TestReloadPicksUpSkills(t *testing.T) {
	agentDir := setupPromptEnv(t)
	a, err := New(configWithBuiltin(openai.ProviderID, openai.DefaultModel), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := a.coordinator.PromptSections()["skills"]; ok {
		t.Fatal("skills section present without any skill")
	}

	writePromptFile(t, filepath.Join(agentDir, "skills", "demo", "SKILL.md"),
		"---\nname: demo\ndescription: Demo skill\n---\n\nbody\n")
	a.Reload()

	skills := a.coordinator.PromptSections()["skills"]
	if skills == "" || !strings.Contains(skills, "demo") {
		t.Errorf("skills = %q, want reloaded skill listed", skills)
	}
}
