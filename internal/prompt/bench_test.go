package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBenchFile 写入基准夹具文件并创建父目录
func writeBenchFile(tb testing.TB, path, content string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatal(err)
	}
}

// benchSkillsFixture 创建接近真实规模的 AGENTS.md 链与 skills 目录
func benchSkillsFixture(tb testing.TB) (cwd, agentDir string) {
	tb.Helper()
	root := tb.TempDir()
	agentDir = filepath.Join(root, "agent")
	cwd = filepath.Join(root, "project")
	writeBenchFile(tb, filepath.Join(agentDir, "AGENTS.md"), strings.Repeat("# global rule\n", 100))
	writeBenchFile(tb, filepath.Join(cwd, "AGENTS.md"), strings.Repeat("# project rule\n", 400))
	skillsDir := filepath.Join(agentDir, "skills")
	for i := range 7 {
		body := strings.Repeat("skill body line\n", 500+i*300)
		content := fmt.Sprintf("---\nname: skill-%d\ndescription: Benchmark skill %d\n---\n\n%s", i, i, body)
		writeBenchFile(tb, filepath.Join(skillsDir, fmt.Sprintf("skill-%d", i), "SKILL.md"), content)
	}
	return cwd, agentDir
}

func BenchmarkLoaderReload(b *testing.B) {
	cwd, agentDir := benchSkillsFixture(b)
	loader := NewLoader(cwd, agentDir)
	b.ResetTimer()
	for b.Loop() {
		loader.Reload()
	}
}

func BenchmarkReadSkillBody(b *testing.B) {
	_, agentDir := benchSkillsFixture(b)
	path := filepath.Join(agentDir, "skills", "skill-0", "SKILL.md")
	b.ResetTimer()
	for b.Loop() {
		if _, err := ReadSkillBody(path); err != nil {
			b.Fatal(err)
		}
	}
}

// TestBenchFixtureLoads 确认基准夹具本身能被解析，避免基准测空目录
func TestBenchFixtureLoads(t *testing.T) {
	cwd, agentDir := benchSkillsFixture(t)
	loader := NewLoader(cwd, agentDir)
	loader.Reload()
	if len(loader.ContextFiles()) != 2 {
		t.Fatalf("context files = %d, want 2", len(loader.ContextFiles()))
	}
	if len(loader.Skills()) != 7 {
		t.Fatalf("skills = %d, want 7", len(loader.Skills()))
	}
}
