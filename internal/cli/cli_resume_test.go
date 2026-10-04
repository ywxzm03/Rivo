package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/session"
)

// useTestEnv 造一个空的 .env 并配好最小环境变量，ConfigFromEnv 才能装配成功
func useTestEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, key := range []string{"PROVIDER", "BASE_URL", "MODEL"} {
		t.Setenv(key, "")
	}
	t.Setenv("API_KEY", "test-key")
}

func TestRunContinueWithoutSessionsStartsNewRepl(t *testing.T) {
	useTestEnv(t)

	for _, arg := range []string{"--continue", "--resume"} {
		var stdout, stderr bytes.Buffer
		code := Run([]string{arg}, strings.NewReader("/exit\n"), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("Run(%q) code = %d, want 0", arg, code)
		}
		// 无会话时静默新建，直接进入 REPL，无恢复输出
		if !strings.Contains(stdout.String(), "> ") {
			t.Errorf("Run(%q) stdout = %q, want REPL prompt", arg, stdout.String())
		}
		if stderr.String() != "" {
			t.Errorf("Run(%q) stderr = %q, want empty", arg, stderr.String())
		}
	}
}

func TestRunHelpTakesPrecedenceOverContinue(t *testing.T) {
	useTestEnv(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--help", "--continue"}, strings.NewReader(""), &stdout, &stderr)
	// 显式请求帮助时优先展示 help，不进入 REPL
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage") {
		t.Errorf("stdout = %q, want help text", stdout.String())
	}
	if strings.Contains(stdout.String(), "> ") {
		t.Errorf("stdout = %q, want no REPL prompt", stdout.String())
	}
}

func TestRunREPLResumeEmptyList(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := runREPL(strings.NewReader("/resume\n/exit\n"), &stdout, &stderr,
		func(_ context.Context, input string) error { return nil },
		nil,
		func() ([]session.SessionInfo, error) { return nil, nil },
		func(path string) error {
			t.Errorf("resume called with %q, want no call", path)
			return nil
		},
	)

	if code != 0 {
		t.Fatalf("runREPL() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "no sessions to resume") {
		t.Errorf("stdout = %q, want empty-list notice", stdout.String())
	}
}

func TestRunREPLResumeSelectsByNumber(t *testing.T) {
	var stdout, stderr bytes.Buffer
	infos := []session.SessionInfo{
		{Path: "/s/new.jsonl", ModTime: time.Now().Add(-time.Minute), Summary: "newest"},
		{Path: "/s/old.jsonl", ModTime: time.Now().Add(-2 * time.Hour), Summary: "older"},
	}
	var resumed string

	code := runREPL(strings.NewReader("/resume\n2\n/exit\n"), &stdout, &stderr,
		func(_ context.Context, input string) error { return nil },
		nil,
		func() ([]session.SessionInfo, error) { return infos, nil },
		func(path string) error {
			resumed = path
			return nil
		},
	)

	if code != 0 {
		t.Fatalf("runREPL() code = %d, want 0", code)
	}
	if resumed != "/s/old.jsonl" {
		t.Errorf("resumed = %q, want /s/old.jsonl", resumed)
	}
	// 列表按编号打印并带相对时间，恢复本身静默
	if !strings.Contains(stdout.String(), "1. ") || !strings.Contains(stdout.String(), "2h ago") {
		t.Errorf("stdout = %q, want numbered list with relative time", stdout.String())
	}
	if !strings.Contains(stdout.String(), "older") {
		t.Errorf("stdout = %q, want session summary", stdout.String())
	}
}

func TestRunREPLResumeInvalidInputCancels(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
	}{
		{name: "empty line", input: "/resume\n\n/exit\n"},
		{name: "not a number", input: "/resume\nabc\n/exit\n"},
		{name: "out of range", input: "/resume\n9\n/exit\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			infos := []session.SessionInfo{{Path: "/s/one.jsonl", ModTime: time.Now(), Summary: "only"}}

			code := runREPL(strings.NewReader(tt.input), &stdout, &stderr,
				func(_ context.Context, input string) error { return nil },
				nil,
				func() ([]session.SessionInfo, error) { return infos, nil },
				func(path string) error {
					t.Errorf("resume called with %q, want no call", path)
					return nil
				},
			)

			if code != 0 {
				t.Fatalf("runREPL() code = %d, want 0", code)
			}
			if stderr.String() != "" {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunREPLResumeErrorStaysInSession(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sentinel := errors.New("restore failed")
	infos := []session.SessionInfo{{Path: "/s/one.jsonl", ModTime: time.Now(), Summary: "only"}}

	code := runREPL(strings.NewReader("/resume\n1\nhello\n/exit\n"), &stdout, &stderr,
		func(_ context.Context, input string) error {
			if input != "hello" {
				t.Errorf("input = %q, want hello", input)
			}
			return nil
		},
		nil,
		func() ([]session.SessionInfo, error) { return infos, nil },
		func(path string) error { return sentinel },
	)

	if code != 0 {
		t.Fatalf("runREPL() code = %d, want 0", code)
	}
	// 恢复失败打印错误后留在当前会话，继续处理后续输入
	if !strings.Contains(stderr.String(), "restore failed") {
		t.Errorf("stderr = %q, want restore failure", stderr.String())
	}
}

func TestRunREPLResumeListErrorStaysInSession(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sentinel := errors.New("list failed")

	code := runREPL(strings.NewReader("/resume\nhello\n/exit\n"), &stdout, &stderr,
		func(_ context.Context, input string) error { return nil },
		nil,
		func() ([]session.SessionInfo, error) { return nil, sentinel },
		func(path string) error {
			t.Errorf("resume called with %q, want no call", path)
			return nil
		},
	)

	if code != 0 {
		t.Fatalf("runREPL() code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "list failed") {
		t.Errorf("stderr = %q, want list failure", stderr.String())
	}
}

func TestRelativeTimeFutureFallsBackToDate(t *testing.T) {
	future := time.Now().Add(time.Hour)
	if got := relativeTime(future); got != future.Format("2006-01-02") {
		t.Errorf("relativeTime(future) = %q, want date %q", got, future.Format("2006-01-02"))
	}
}

func TestRunUnknownArgBeforeHelpStillShowsHelp(t *testing.T) {
	useTestEnv(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--bad", "--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage") {
		t.Errorf("stdout = %q, want help text", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}
