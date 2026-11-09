package cli_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/cli"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// useTestHome 把 HOME/USERPROFILE/RIVO_AGENT_DIR 都切到临时目录，跨平台隔离真实配置
func useTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("RIVO_AGENT_DIR", home+string(os.PathSeparator)+".rivo-agent-test")
}

func TestRun(t *testing.T) {
	useTestHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "test-key")

	tests := []struct {
		name       string
		args       []string
		input      string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "long help",
			args:       []string{"--help"},
			wantCode:   0,
			wantStdout: "",
		},
		{
			name:       "short help",
			args:       []string{"-h"},
			wantCode:   0,
			wantStdout: "",
		},
		{
			name:       "repl exits",
			input:      "/exit\n",
			wantCode:   0,
			wantStdout: "> ",
		},
		{
			name:       "unknown long option",
			args:       []string{"--unknown"},
			wantCode:   2,
			wantStderr: "Rivo: unsupported argument \"--unknown\"\nRun 'Rivo --help' for usage.\n",
		},
		{
			name:       "help with unknown option",
			args:       []string{"--help", "--unknown"},
			wantCode:   0,
			wantStdout: "",
		},
		{
			name:       "long version",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: "Rivo " + cli.Version + "\n",
		},
		{
			name:       "short version",
			args:       []string{"-v"},
			wantCode:   0,
			wantStdout: "Rivo " + cli.Version + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := cli.Run(tt.args, strings.NewReader(tt.input), &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("Run() code = %d, want %d", code, tt.wantCode)
			}
			if strings.Contains(tt.name, "help") {
				for _, want := range []string{"Usage:", "--session <path|id>", "--session-id <id>"} {
					if !strings.Contains(stdout.String(), want) {
						t.Errorf("Run() help stdout = %q, want %q", stdout.String(), want)
					}
				}
			} else if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("Run() stdout = %q, want %q", got, tt.wantStdout)
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Errorf("Run() stderr = %q, want %q", got, tt.wantStderr)
			}
		})
	}
}

func TestRunVersionUsesInjectedVersion(t *testing.T) {
	useTestHome(t)
	old := cli.Version
	cli.Version = "1.2.3"
	t.Cleanup(func() { cli.Version = old })

	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"--version"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0", code)
	}
	if got := stdout.String(); got != "Rivo 1.2.3\n" {
		t.Errorf("Run() stdout = %q, want %q", got, "Rivo 1.2.3\n")
	}
	if stderr.Len() != 0 {
		t.Errorf("Run() stderr = %q, want empty", stderr.String())
	}
}

func TestRunNoModelsAvailable(t *testing.T) {
	useTestHome(t)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Run(nil, strings.NewReader("/exit\n"), &stdout, &stderr)

	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("Run() stdout = %q, want empty", got)
	}
	if !strings.Contains(stderr.String(), "no models available") {
		t.Errorf("Run() stderr = %q, want no-models diagnostic", stderr.String())
	}
}

func TestRunCostReadsSessionWithoutRuntimeConfig(t *testing.T) {
	m := session.New("/project", t.TempDir())
	if err := m.AppendMessage(llm.AssistantMessage{Usage: llm.Usage{
		InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40, TotalTokens: 100,
		Cost: &llm.Cost{Total: 1.25},
	}}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"--cost", m.Path()}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	want := "Cost (USD): $1.250000\n" +
		"Input tokens: 10\n" +
		"Output tokens: 20\n" +
		"Cache read tokens: 30\n" +
		"Cache write tokens: 40\n" +
		"Total tokens: 100\n" +
		"Unknown cost entries: 0\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunCostRejectsRuntimeOptions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"--cost", "session.jsonl", "--print"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "cannot be combined") {
		t.Fatalf("Run() = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunCostDoesNotDeleteEmptySessionFile(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "empty.jsonl"
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"--cost", path}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("empty session file was removed: %v", err)
	}
	want := "Cost (USD): $0.000000\n" +
		"Input tokens: 0\n" +
		"Output tokens: 0\n" +
		"Cache read tokens: 0\n" +
		"Cache write tokens: 0\n" +
		"Total tokens: 0\n" +
		"Unknown cost entries: 0\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}
