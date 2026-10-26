package support

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Process 是一次被测试的 rivo 进程
type Process struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  bytes.Buffer
	done chan error
}

// StartBinary 用隔离环境启动编译好的 rivo 二进制
func StartBinary(t *testing.T, env Env, args ...string) *Process {
	t.Helper()
	cmd := exec.Command(env.Binary, args...)
	cmd.Dir = env.WorkDir
	cmd.Env = env.Environ()
	cmd.Stdin = strings.NewReader("")
	process := &Process{cmd: cmd, done: make(chan error, 1)}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		t.Fatalf("start rivo: %v", err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				process.mu.Lock()
				process.out.Write(buf[:n])
				process.mu.Unlock()
			}
			if err != nil {
				break
			}
		}
		process.done <- cmd.Wait()
	}()
	t.Cleanup(func() { process.Kill() })
	return process
}

// Output 返回目前已捕获的输出
func (p *Process) Output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

// WaitFor 等待输出中出现指定的文本，超时则失败并打印原始输出
func (p *Process) WaitFor(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(p.Output(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output never contained %q\n--- captured output ---\n%s", want, p.Output())
}

// Wait 等待进程退出并返回退出码
func (p *Process) Wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case err := <-p.done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		t.Fatalf("process failed: %v\n--- captured output ---\n%s", err, p.Output())
		return -1
	case <-time.After(timeout):
		p.Kill()
		t.Fatalf("process did not exit within %v\n--- captured output ---\n%s", timeout, p.Output())
		return -1
	}
}

// Kill 强制结束进程，已退出时安全
func (p *Process) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// Env 是一次 E2E 的隔离环境
type Env struct {
	// Binary 是编译好的 rivo 可执行文件
	Binary string
	// AgentDir 是 RIVO_AGENT_DIR
	AgentDir string
	// Home 是被替换的 HOME
	Home string
	// WorkDir 是工作目录
	WorkDir string
}

// NewEnv 创建隔离环境并编译二进制，产物缓存在进程级临时目录
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
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	env.Binary = buildBinary(t)
	return env
}

// Environ 返回子进程环境：隔离 HOME 与 agent 目录，并清空真实凭据
func (e Env) Environ() []string {
	env := append([]string(nil), os.Environ()...)
	env = append(env,
		"HOME="+e.Home,
		"USERPROFILE="+e.Home,
		"RIVO_AGENT_DIR="+e.AgentDir,
		"DEEPSEEK_API_KEY=",
		"OPENAI_API_KEY=",
	)
	return env
}

// WriteModels 写入指向假模型的 models.json，并把该 provider 设为默认
func (e Env) WriteModels(t *testing.T, baseURL, providerID, modelID string) {
	t.Helper()
	body := `{"providers":{"` + providerID + `":{` +
		`"name":"Scripted","baseUrl":"` + baseURL + `","apiKey":"test-key",` +
		`"api":"openai-responses",` +
		`"models":[{"id":"` + modelID + `","contextWindow":100000,"maxTokens":4096}]}}}`
	if err := os.WriteFile(filepath.Join(e.AgentDir, "models.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}
	settings := `{"provider":"` + providerID + `","model":"` + modelID + `"}`
	if err := os.WriteFile(filepath.Join(e.AgentDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
}

// buildBinary 编译一次 rivo 二进制，同一测试进程内复用
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "rivo")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/rivo")
	cmd.Dir = moduleRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

// moduleRoot 定位仓库根目录
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}
