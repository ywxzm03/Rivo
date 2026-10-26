package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// Env 隔离一次测试的运行环境：独立的 agent 目录、HOME 与工作目录，
// 并清空内置 provider 凭据，避免开发机环境泄漏进断言
type Env struct {
	// AgentDir 是 RIVO_AGENT_DIR 指向的目录
	AgentDir string
	// Home 是被替换的 HOME
	Home string
	// WorkDir 是测试专用的工作目录
	WorkDir string
}

// NewEnv 创建隔离环境并返回其路径；测试结束自动清理
func NewEnv(t *testing.T) Env {
	t.Helper()
	root := t.TempDir()
	env := Env{
		AgentDir: filepath.Join(root, "agent"),
		Home:     filepath.Join(root, "home"),
		WorkDir:  filepath.Join(root, "work"),
	}
	for _, dir := range []string{env.AgentDir, env.Home, env.WorkDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("testutil: create %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", env.Home)
	t.Setenv("USERPROFILE", env.Home)
	t.Setenv("RIVO_AGENT_DIR", env.AgentDir)
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	return env
}

// WriteConfig 在 agent 目录下写入一份配置文件
func (e Env) WriteConfig(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.AgentDir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("testutil: write %s: %v", name, err)
	}
}
