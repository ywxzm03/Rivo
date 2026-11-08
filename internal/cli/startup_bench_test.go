//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/session"
)

// startupMarker 是 Bubble Tea 进入 alt screen 的序列，首帧渲染前的最后一段启动逻辑
const startupMarker = "\x1b[?1049h"

// lockedBuffer 串行化子进程输出与基准轮询的读写
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) contains(data []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// buildStartupBinary 为当前基准构建一次待测二进制，随 b.TempDir 一起清理
func buildStartupBinary(b *testing.B) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "rivo")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", path, "./cmd/rivo")
	cmd.Dir = filepath.Join("..", "..")
	if output, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("go build: %v\n%s", err, output)
	}
	return path
}

// startupFixture 是端到端启动基准的临时 HOME、项目目录与服务端
type startupFixture struct {
	home    string
	project string
	agent   string
}

const startupModelsJSON = `{"providers":{"bench":{"api":"openai-completions","apiKey":"bench","baseUrl":%q,"models":[{"id":"bench-model","name":"Bench Model","contextWindow":100000,"maxTokens":4096}]}}}`

// newStartupFixture 创建临时 HOME、git 项目与 models.json
func newStartupFixture(b *testing.B, serverURL string) startupFixture {
	b.Helper()
	root := b.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	agent := filepath.Join(home, ".rivo")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		b.Fatal(err)
	}
	// 项目目录解析成真实路径，子进程 Getwd 与会话目录编码才能一致
	realProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		b.Fatal(err)
	}
	models := fmt.Sprintf(startupModelsJSON, serverURL)
	if err := os.WriteFile(filepath.Join(agent, "models.json"), []byte(models), 0o644); err != nil {
		b.Fatal(err)
	}
	settings := `{"defaultProvider":"bench","defaultModel":"bench-model"}`
	if err := os.WriteFile(filepath.Join(agent, "settings.json"), []byte(settings), 0o644); err != nil {
		b.Fatal(err)
	}
	// 写入与真实安装同量级的 skills，避免基准只测空目录
	for i := range 7 {
		dir := filepath.Join(agent, "skills", fmt.Sprintf("skill-%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: skill-%d\ndescription: Benchmark skill %d\n---\n\n%s", i, i, strings.Repeat("skill body line\n", 500+i*300))
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	gitInit := exec.Command("git", "init", "-q", realProject)
	gitInit.Env = startupEnv(startupFixture{home: home, project: realProject, agent: agent})
	if output, err := gitInit.CombinedOutput(); err != nil {
		b.Fatalf("git init: %v\n%s", err, output)
	}
	return startupFixture{home: home, project: realProject, agent: agent}
}

// writeContinueSession 在夹具 HOME 下写入一个大会话，供 --continue 读取
func writeContinueSession(b *testing.B, fixture startupFixture, messages int) {
	b.Helper()
	b.Setenv("HOME", fixture.home)
	dir, err := session.DefaultDir(fixture.project)
	if err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	header, err := json.Marshal(map[string]any{"type": "session", "version": 1, "id": "bench-session", "timestamp": "2026-01-01T00:00:00Z", "cwd": fixture.project})
	if err != nil {
		b.Fatal(err)
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
	if err := os.WriteFile(filepath.Join(dir, "2026-01-01T00-00-00Z_bench-session.jsonl"), []byte(builder.String()), 0o644); err != nil {
		b.Fatal(err)
	}
}

// startupEnv 返回子进程环境：临时 HOME、独立 agent 目录与稳定的 TERM
func startupEnv(fixture startupFixture) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		switch {
		case strings.HasPrefix(entry, "HOME="), strings.HasPrefix(entry, "RIVO_AGENT_DIR="), strings.HasPrefix(entry, "TERM="),
			strings.HasPrefix(entry, "GIT_DIR="), strings.HasPrefix(entry, "GIT_WORK_TREE="),
			strings.HasPrefix(entry, "GIT_CEILING_DIRECTORIES="), strings.HasPrefix(entry, "GIT_DISCOVERY_ACROSS_FILESYSTEM="):
			continue
		}
		env = append(env, entry)
	}
	return append(env, "HOME="+fixture.home, "RIVO_AGENT_DIR="+fixture.agent, "TERM=xterm-256color")
}

// startPTY 用系统 script 命令给子进程分配 PTY，返回可读输出
func startPTY(b *testing.B, binary string, args []string, dir string, env []string) (*exec.Cmd, *lockedBuffer, error) {
	b.Helper()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("script", append([]string{"-q", "/dev/null", binary}, args...)...)
	case "linux":
		quoted := make([]string, 0, len(args)+1)
		quoted = append(quoted, binary)
		quoted = append(quoted, args...)
		cmd = exec.Command("script", "-q", "-c", strings.Join(quoted, " "), "/dev/null")
	default:
		b.Skip("PTY startup benchmark supports darwin and linux only")
	}
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &lockedBuffer{}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, out, nil
}

// waitForMarker 等待输出里出现 alt screen 标记，超时返回 false
func waitForMarker(out *lockedBuffer, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if out.contains([]byte(startupMarker)) {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// killGroup 结束 script 及其子进程
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

// BenchmarkStartupTUI 测量新建会话从 exec 到 alt screen 的时间
func BenchmarkStartupTUI(b *testing.B) {
	binary := buildStartupBinary(b)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	fixture := newStartupFixture(b, server.URL+"/v1")
	env := startupEnv(fixture)
	// 先跑一次热启动，排除二进制首次 page-in
	warmCmd, warmOut, err := startPTY(b, binary, nil, fixture.project, env)
	if err != nil {
		b.Fatal(err)
	}
	if !waitForMarker(warmOut, 20*time.Second) {
		killGroup(warmCmd)
		b.Fatalf("startup marker not found: %q", warmOut.String())
	}
	killGroup(warmCmd)
	var total time.Duration
	var count int
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		cmd, out, err := startPTY(b, binary, nil, fixture.project, env)
		if err != nil {
			b.Fatal(err)
		}
		if !waitForMarker(out, 10*time.Second) {
			killGroup(cmd)
			b.Fatalf("startup marker not found: %q", out.String())
		}
		total += time.Since(start)
		count++
		killGroup(cmd)
	}
	b.ReportMetric(float64(total.Microseconds())/1000/float64(count), "ms/alt-screen")
}

// BenchmarkStartupContinue 测量 --continue 读取大会话到 alt screen 的时间
func BenchmarkStartupContinue(b *testing.B) {
	binary := buildStartupBinary(b)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	fixture := newStartupFixture(b, server.URL+"/v1")
	writeContinueSession(b, fixture, 4000)
	env := startupEnv(fixture)
	warmCmd, warmOut, err := startPTY(b, binary, []string{"--continue"}, fixture.project, env)
	if err != nil {
		b.Fatal(err)
	}
	if !waitForMarker(warmOut, 20*time.Second) {
		killGroup(warmCmd)
		b.Fatalf("startup marker not found: %q", warmOut.String())
	}
	killGroup(warmCmd)
	var total time.Duration
	var count int
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		cmd, out, err := startPTY(b, binary, []string{"--continue"}, fixture.project, env)
		if err != nil {
			b.Fatal(err)
		}
		if !waitForMarker(out, 10*time.Second) {
			killGroup(cmd)
			b.Fatalf("startup marker not found: %q", out.String())
		}
		total += time.Since(start)
		count++
		killGroup(cmd)
	}
	b.ReportMetric(float64(total.Microseconds())/1000/float64(count), "ms/alt-screen")
}

// BenchmarkStartupPrint 测量 rivo -p 从 exec 到服务端收到请求的时间
func BenchmarkStartupPrint(b *testing.B) {
	binary := buildStartupBinary(b)
	requests := make(chan time.Time, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- time.Now():
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"bench-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n",
			"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"bench-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			"data: [DONE]\n\n",
		)
	}))
	defer server.Close()
	fixture := newStartupFixture(b, server.URL+"/v1")
	env := startupEnv(fixture)
	var total time.Duration
	var count int
	b.ResetTimer()
	for b.Loop() {
		select {
		case <-requests:
		default:
		}
		cmd := exec.Command(binary, "-p", "hi")
		cmd.Dir = fixture.project
		cmd.Env = env
		start := time.Now()
		if err := cmd.Start(); err != nil {
			b.Fatal(err)
		}
		var elapsed time.Duration
		select {
		case arrived := <-requests:
			elapsed = arrived.Sub(start)
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			b.Fatal("request not received")
		}
		total += elapsed
		count++
		_ = cmd.Wait()
	}
	b.ReportMetric(float64(total.Microseconds())/1000/float64(count), "ms/to-request")
}
