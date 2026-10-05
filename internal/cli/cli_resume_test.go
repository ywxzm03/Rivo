package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// useTestEnv 隔离真实配置并配好内置 provider 凭据
func useTestEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("RIVO_AGENT_DIR", home+string(os.PathSeparator)+".rivo-agent-test")
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
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
	if stdout.String() != helpText {
		t.Errorf("stdout = %q, want exactly the help text", stdout.String())
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

func TestBuildPrintPromptJoinsPositional(t *testing.T) {
	prompt, err := buildPrintPrompt([]string{"hello", "world"}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("buildPrintPrompt() error = %v", err)
	}
	if prompt != "hello world" {
		t.Errorf("prompt = %q, want %q", prompt, "hello world")
	}
}

func TestBuildPrintPromptReadsPipedStdin(t *testing.T) {
	prompt, err := buildPrintPrompt(nil, strings.NewReader("piped prompt\n"))
	if err != nil {
		t.Fatalf("buildPrintPrompt() error = %v", err)
	}
	if prompt != "piped prompt" {
		t.Errorf("prompt = %q, want %q", prompt, "piped prompt")
	}
}

func TestBuildPrintPromptEmptyStdinErrors(t *testing.T) {
	if _, err := buildPrintPrompt(nil, strings.NewReader("  \n")); err == nil {
		t.Fatal("buildPrintPrompt() error = nil, want empty-prompt error")
	}
}

func TestRunProviderWithoutModelErrors(t *testing.T) {
	useTestEnv(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--provider", "openai"}, strings.NewReader("/exit\n"), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--provider requires --model") {
		t.Errorf("stderr = %q, want provider-requires-model error", stderr.String())
	}
}

func TestRunInvalidThinkingErrors(t *testing.T) {
	useTestEnv(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--thinking", "ultra"}, strings.NewReader("/exit\n"), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown thinking level") {
		t.Errorf("stderr = %q, want unknown thinking level error", stderr.String())
	}
}

func TestRunPositionalWithoutPrintErrors(t *testing.T) {
	useTestEnv(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"hello"}, strings.NewReader("/exit\n"), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unsupported argument") {
		t.Errorf("stderr = %q, want unsupported argument error", stderr.String())
	}
}

// fakeRunner 实现 promptRunner，记录输入并返回预设结果
type fakeRunner struct {
	input string
	msg   *llm.AssistantMessage
	err   error
}

func (f *fakeRunner) Run(_ context.Context, input string) (*llm.AssistantMessage, error) {
	f.input = input
	return f.msg, f.err
}

func TestPrintModeOutputsAssistantText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{msg: &llm.AssistantMessage{
		Content:    []llm.Content{llm.TextContent{Text: "the answer"}},
		StopReason: llm.StopReasonStop,
	}}

	code := printMode(app.Config{}, "question", false, &stdout, &stderr, runner)
	if code != 0 {
		t.Fatalf("printMode() code = %d, want 0", code)
	}
	if runner.input != "question" {
		t.Errorf("input = %q, want %q", runner.input, "question")
	}
	if stdout.String() != "the answer\n" {
		t.Errorf("stdout = %q, want the answer text", stdout.String())
	}
}

func TestPrintModeRunErrorReturnsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{err: errors.New("boom")}

	code := printMode(app.Config{}, "q", false, &stdout, &stderr, runner)
	if code != 1 {
		t.Fatalf("printMode() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr = %q, want run error", stderr.String())
	}
}

func TestPrintModeErrorStopReasonReturnsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{msg: &llm.AssistantMessage{StopReason: llm.StopReasonError}}

	code := printMode(app.Config{}, "q", false, &stdout, &stderr, runner)
	if code != 1 {
		t.Fatalf("printMode() code = %d, want 1", code)
	}
}

func TestRunMissingFlagValueErrors(t *testing.T) {
	useTestEnv(t)
	var stdout, stderr bytes.Buffer
	// --model 的值缺位（下一个 token 是另一个 flag）时报缺值错误，而不是吞掉后面的 flag
	code := Run([]string{"--model", "--thinking", "high"}, strings.NewReader("/exit\n"), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "flag --model requires a value") {
		t.Errorf("stderr = %q, want missing-value error for --model", stderr.String())
	}
}
