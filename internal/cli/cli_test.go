package cli_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/cli"
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
			wantStdout: "Rivo - AI coding assistant\n\nUsage:\n  Rivo [options] [prompt]\n\nOptions:\n  -h, --help              Show this help\n      --continue          Continue the most recent session\n      --resume            Alias of --continue\n      --print, -p         Non-interactive mode: process prompt and exit\n      --provider <name>   Provider for --model (requires --model)\n      --model <pattern>   Model id, provider/model, or substring, with optional :level suffix\n      --thinking <level>  Thinking level: off, minimal, low, medium, high, xhigh, max\n\nWith --print, the prompt comes from positional arguments or piped stdin.\n",
		},
		{
			name:       "short help",
			args:       []string{"-h"},
			wantCode:   0,
			wantStdout: "Rivo - AI coding assistant\n\nUsage:\n  Rivo [options] [prompt]\n\nOptions:\n  -h, --help              Show this help\n      --continue          Continue the most recent session\n      --resume            Alias of --continue\n      --print, -p         Non-interactive mode: process prompt and exit\n      --provider <name>   Provider for --model (requires --model)\n      --model <pattern>   Model id, provider/model, or substring, with optional :level suffix\n      --thinking <level>  Thinking level: off, minimal, low, medium, high, xhigh, max\n\nWith --print, the prompt comes from positional arguments or piped stdin.\n",
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
			wantStdout: "Rivo - AI coding assistant\n\nUsage:\n  Rivo [options] [prompt]\n\nOptions:\n  -h, --help              Show this help\n      --continue          Continue the most recent session\n      --resume            Alias of --continue\n      --print, -p         Non-interactive mode: process prompt and exit\n      --provider <name>   Provider for --model (requires --model)\n      --model <pattern>   Model id, provider/model, or substring, with optional :level suffix\n      --thinking <level>  Thinking level: off, minimal, low, medium, high, xhigh, max\n\nWith --print, the prompt comes from positional arguments or piped stdin.\n",
		},
		{
			name:       "version is not implemented",
			args:       []string{"--version"},
			wantCode:   2,
			wantStderr: "Rivo: unsupported argument \"--version\"\nRun 'Rivo --help' for usage.\n",
		},
		{
			name:       "short version is not implemented",
			args:       []string{"-v"},
			wantCode:   2,
			wantStderr: "Rivo: unsupported argument \"-v\"\nRun 'Rivo --help' for usage.\n",
		},
		{
			name:       "positional argument is not implemented",
			args:       []string{"hello"},
			wantCode:   2,
			wantStderr: "Rivo: unsupported argument \"hello\"\nRun 'Rivo --help' for usage.\n",
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
			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("Run() stdout = %q, want %q", got, tt.wantStdout)
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Errorf("Run() stderr = %q, want %q", got, tt.wantStderr)
			}
		})
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
