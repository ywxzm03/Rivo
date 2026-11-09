package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/provider"
)

// benchProviderConfig 用临时 HOME 与 agent 目录加载一份固定 provider 配置
func benchProviderConfig(tb testing.TB) provider.Config {
	tb.Helper()
	home := tb.TempDir()
	agentDir := filepath.Join(home, ".rivo")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		tb.Fatal(err)
	}
	models := `{"providers":{"bench":{"api":"openai-completions","apiKey":"bench","baseUrl":"http://127.0.0.1:1/v1","models":[{"id":"bench-model","name":"Bench Model","contextWindow":100000,"maxTokens":4096}]}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(models), 0o644); err != nil {
		tb.Fatal(err)
	}
	settings := `{"defaultProvider":"bench","defaultModel":"bench-model"}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
		tb.Fatal(err)
	}
	// 写入与真实安装同量级的 skills，避免基准只测空目录
	for i := range 7 {
		dir := filepath.Join(agentDir, "skills", fmt.Sprintf("skill-%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: skill-%d\ndescription: Benchmark skill %d\n---\n\n%s", i, i, strings.Repeat("skill body line\n", 500+i*300))
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	tb.Setenv("HOME", home)
	tb.Setenv("RIVO_AGENT_DIR", agentDir)
	cfg, err := provider.Load()
	if err != nil {
		tb.Fatal(err)
	}
	return cfg
}

// benchProject 创建固定项目目录并切换工作目录，避免读到开发者仓库的 AGENTS.md
func benchProject(tb testing.TB) string {
	tb.Helper()
	project := tb.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("# bench project\n"), 0o644); err != nil {
		tb.Fatal(err)
	}
	tb.Chdir(project)
	return project
}

// writeBenchSessionFile 写入一个约 size 字节的合法会话文件
func writeBenchSessionFile(tb testing.TB, dir string, messages int, cwd string) string {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	header, err := json.Marshal(map[string]any{"type": "session", "version": 1, "id": "bench-session", "timestamp": "2026-01-01T00:00:00Z", "cwd": cwd})
	if err != nil {
		tb.Fatal(err)
	}
	var builder strings.Builder
	builder.Write(header)
	builder.WriteByte('\n')
	text := strings.Repeat("x", 900)
	parent := "null"
	for i := range messages {
		id := fmt.Sprintf("bench-%d", i)
		fmt.Fprintf(&builder, `{"type":"message","id":%q,"parentId":%s,"timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n", id, parent, text)
		parent = fmt.Sprintf("%q", id)
	}
	path := filepath.Join(dir, "2026-01-01T00-00-00Z_bench-session.jsonl")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		tb.Fatal(err)
	}
	return path
}

func BenchmarkNewApp(b *testing.B) {
	benchProject(b)
	cfg := benchProviderConfig(b)
	b.ResetTimer()
	for b.Loop() {
		a, err := New(cfg, nil)
		if err != nil {
			b.Fatal(err)
		}
		a.Close()
	}
}

func BenchmarkOpenApp(b *testing.B) {
	project := benchProject(b)
	cfg := benchProviderConfig(b)
	path := writeBenchSessionFile(b, filepath.Join(b.TempDir(), "sessions"), 4000, project)
	b.ResetTimer()
	for b.Loop() {
		a, err := Open(path, cfg, nil)
		if err != nil {
			b.Fatal(err)
		}
		a.Close()
	}
}
