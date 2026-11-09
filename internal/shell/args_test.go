package shell

import (
	"math"
	"strings"
	"testing"
	"time"
)

// ParseArgs 是 shell 类工具共用的参数解析，边界值必须全部覆盖
func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		want    time.Duration
		wantErr string
	}{
		{"仅 command", map[string]any{"command": "ls"}, 0, ""},
		{"带 timeout", map[string]any{"command": "ls", "timeout": 1.5}, 1500 * time.Millisecond, ""},
		{"timeout 为 0", map[string]any{"command": "ls", "timeout": 0.0}, 0, "greater than 0"},
		{"timeout 为负", map[string]any{"command": "ls", "timeout": -1.0}, 0, "greater than 0"},
		{"timeout 为 NaN", map[string]any{"command": "ls", "timeout": math.NaN()}, 0, "finite"},
		{"timeout 为 Inf", map[string]any{"command": "ls", "timeout": math.Inf(1)}, 0, "finite"},
		{"timeout 非数字", map[string]any{"command": "ls", "timeout": "5"}, 0, "finite"},
		{"timeout 超上限", map[string]any{"command": "ls", "timeout": 3_000_000.0}, 0, "maximum is"},
		{"command 缺失", map[string]any{}, 0, "non-empty string"},
		{"command 为空串", map[string]any{"command": ""}, 0, "non-empty string"},
		{"command 类型错误", map[string]any{"command": 42}, 0, "non-empty string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command, timeout, err := ParseArgs(tt.args, "bash")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseArgs() error = %v, want nil", err)
				}
				if timeout != tt.want {
					t.Errorf("timeout = %v, want %v", timeout, tt.want)
				}
				if command == "" {
					t.Error("command = empty, want the parsed command")
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseArgs() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "bash:") {
				t.Errorf("error = %q, want the tool name prefix", err)
			}
		})
	}
}

// FormatTimeout 去掉多余小数零
func TestFormatTimeout(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{1, "1"},
		{1.5, "1.5"},
		{0.25, "0.25"},
		{2147483.647, "2147483.647"},
	}
	for _, tt := range tests {
		if got := FormatTimeout(tt.in); got != tt.want {
			t.Errorf("FormatTimeout(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
