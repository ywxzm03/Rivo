package tool

import (
	"strings"
	"testing"
)

// 灾难性命令必须被拦截,其余命令放行
func TestGuardShellCommand(t *testing.T) {
	blocked := []struct{ command, want string }{
		{"rm -rf /", "critical path"},
		{"rm -rf /*", "critical path"},
		{"rm -rf ~", "critical path"},
		{"rm -fr ..", "critical path"},
		{"ls; rm -rf /", "critical path"},
		{"echo x | rm -rf /", "critical path"},
		{"dd if=/dev/zero of=/dev/sda", "block device"},
		{"zmodload zsh/files", "zsh module"},
		{"zpty ls", "zsh module"},
		{"zf_rm -rf /", "zsh module"},
	}
	for _, test := range blocked {
		err := guardShellCommand(test.command)
		if err == nil {
			t.Errorf("guardShellCommand(%q) = nil, want error", test.command)
			continue
		}
		if !strings.Contains(err.Error(), test.want) {
			t.Errorf("guardShellCommand(%q) = %q, want substring %q", test.command, err.Error(), test.want)
		}
	}

	allowed := []string{
		"ls -la", "find . -maxdepth 1", "git status", "git log -n 5",
		"cat main.go | head -20", "echo $(date)", "go test ./... | grep FAIL",
		"rm -rf ./build", "rm build/main.o", "dd if=/dev/zero of=/dev/null count=1",
		"grep -r TODO .", "ps aux", "lsof -i", "ss -tlnp",
		"git branch -m a b", "npm install", "go build ./...",
	}
	for _, command := range allowed {
		if err := guardShellCommand(command); err != nil {
			t.Errorf("guardShellCommand(%q) = %v, want nil", command, err)
		}
	}
}

// 控制字符会被 bash 静默丢弃,必须拦截
func TestGuardShellCommandRejectsControlChars(t *testing.T) {
	for _, command := range []string{"ls\x00", "rm\x01 -rf /", "echo\x7f"} {
		if err := guardShellCommand(command); err == nil {
			t.Errorf("guardShellCommand(%q) = nil, want error", command)
		}
	}
	// 制表符、换行、回车是合法的
	for _, command := range []string{"ls\t-la", "ls -la\n", "ls\r"} {
		if err := guardShellCommand(command); err != nil {
			t.Errorf("guardShellCommand(%q) = %v, want nil", command, err)
		}
	}
}
