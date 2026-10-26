package shell

import (
	"os"
	"strings"
	"testing"
)

// prependPath 只在目录尚未出现时前置，避免重复叠加
func TestPrependPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	tests := []struct {
		name    string
		entry   string
		current string
		want    string
	}{
		{"空 PATH 直接返回目录", "/bin/rivo", "", "/bin/rivo"},
		{"未出现时前置", "/bin/rivo", "/usr/bin", "/bin/rivo" + sep + "/usr/bin"},
		{"已出现时不重复", "/bin/rivo", "/usr/bin" + sep + "/bin/rivo", "/usr/bin" + sep + "/bin/rivo"},
		{"首位已存在时保持原样", "/bin/rivo", "/bin/rivo" + sep + "/usr/bin", "/bin/rivo" + sep + "/usr/bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prependPath(tt.entry, tt.current); got != tt.want {
				t.Errorf("prependPath(%q, %q) = %q, want %q", tt.entry, tt.current, got, tt.want)
			}
		})
	}
}

// Environment 必须把 rivo bin 目录放进 PATH，且只出现一次
func TestEnvironmentPrependsBinDir(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	env := Environment()
	var path string
	count := 0
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, "PATH") {
			path = value
			count++
		}
	}
	if count != 1 {
		t.Fatalf("PATH entries = %d, want exactly 1", count)
	}
	if !strings.Contains(path, "rivo") {
		t.Errorf("PATH = %q, want the rivo bin dir prepended", path)
	}
	if !strings.HasSuffix(path, "usr/bin") && !strings.Contains(path, "usr/bin") {
		t.Errorf("PATH = %q, want the original value preserved", path)
	}
}
