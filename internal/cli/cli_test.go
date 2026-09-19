package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/cli"
)

func TestRun(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("RIVO_MODEL", "gpt-5.6-sol")

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
			wantStdout: "Rivo - AI coding assistant\n\nUsage:\n  Rivo [options]\n\nOptions:\n  -h, --help  Show this help\n",
		},
		{
			name:       "short help",
			args:       []string{"-h"},
			wantCode:   0,
			wantStdout: "Rivo - AI coding assistant\n\nUsage:\n  Rivo [options]\n\nOptions:\n  -h, --help  Show this help\n",
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
			wantStderr: "Rivo: 不支持的参数 \"--unknown\"\n请运行 'Rivo --help' 查看用法。\n",
		},
		{
			name:       "help with unknown option",
			args:       []string{"--help", "--unknown"},
			wantCode:   2,
			wantStderr: "Rivo: 不支持的参数 \"--unknown\"\n请运行 'Rivo --help' 查看用法。\n",
		},
		{
			name:       "version is not implemented",
			args:       []string{"--version"},
			wantCode:   2,
			wantStderr: "Rivo: 不支持的参数 \"--version\"\n请运行 'Rivo --help' 查看用法。\n",
		},
		{
			name:       "short version is not implemented",
			args:       []string{"-v"},
			wantCode:   2,
			wantStderr: "Rivo: 不支持的参数 \"-v\"\n请运行 'Rivo --help' 查看用法。\n",
		},
		{
			name:       "positional argument is not implemented",
			args:       []string{"hello"},
			wantCode:   2,
			wantStderr: "Rivo: 不支持的参数 \"hello\"\n请运行 'Rivo --help' 查看用法。\n",
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

func TestRunMissingAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Run(nil, strings.NewReader("/exit\n"), &stdout, &stderr)

	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("Run() stdout = %q, want empty", got)
	}
	if got := stderr.String(); got != "Rivo: OPENAI_API_KEY 未设置\n" {
		t.Errorf("Run() stderr = %q, want missing-key diagnostic", got)
	}
}
