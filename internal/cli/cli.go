package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

const helpText = `Rivo - AI coding assistant

Usage:
  Rivo [options] [prompt]

Options:
  -h, --help              Show this help
      --cost <path>       Print cost totals for a JSONL session and exit
      --continue          Continue the most recent session
      --resume            Alias of --continue
      --session <path|id> Open a session by path, exact ID, or ID prefix
      --session-id <id>   Open an exact project ID, or create it when missing
      --print, -p         Non-interactive mode: process prompt and exit
      --provider <name>   Provider for --model (requires --model)
      --model <pattern>   Model id, provider/model, or substring, with optional :level suffix
      --thinking <level>  Thinking level: off, minimal, low, medium, high, xhigh, max

With a terminal, a positional prompt is submitted automatically in the TUI.
Without a terminal, a positional prompt runs once and exits.
With --print, the prompt comes from positional arguments or piped stdin.
`

func Repl(stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := app.Load()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	return repl(cfg, false, stdin, stdout, stderr)
}

// repl 组装 Agent 并进入交互模式，continueRecent 为 true 时恢复最近会话
func repl(cfg app.Config, continueRecent bool, stdin io.Reader, stdout, stderr io.Writer) int {
	target := sessionTarget{kind: targetNew}
	if continueRecent {
		target.kind = targetRecent
	}
	return replTarget(cfg, target, stdin, stdout, stderr)
}

func replTarget(cfg app.Config, target sessionTarget, stdin io.Reader, stdout, stderr io.Writer) int {
	onEvent := func(ev llm.AgentEvent) error {
		if warning, ok := ev.(llm.WarningEvent); ok {
			_, err := fmt.Fprintf(stderr, "[warning] %s\n", warning.Message)
			return err
		}
		if update, ok := ev.(llm.MessageUpdateEvent); ok {
			if text, ok := update.AssistantMessageEvent.(llm.AssistantMessageTextDeltaEvent); ok {
				_, err := fmt.Fprint(stdout, text.Delta)
				return err
			}
		}
		// 压缩完成打印一行反馈；手动压缩的失败交给 runREPL 的错误路径，避免重复输出
		if end, ok := ev.(llm.CompactionEndEvent); ok {
			if end.Error != "" {
				if end.Reason != "manual" {
					if _, err := fmt.Fprintf(stderr, "[context compaction failed (%s): %s]\n", end.Reason, end.Error); err != nil {
						return err
					}
				}
				return nil
			}
			preview := end.Summary
			// 按 rune 截断，避免把多字节字符从中间切开
			if runes := []rune(preview); len(runes) > 80 {
				preview = string(runes[:80]) + "..."
			}
			if _, err := fmt.Fprintf(stdout, "\n[context compacted (%s): %d -> %d tokens]\n%s\n", end.Reason, end.TokensBefore, end.TokensAfter, preview); err != nil {
				return err
			}
		}
		return nil
	}

	// 1.按 CLI 目标组装 Agent
	a, err := openSessionTarget(cfg, target, onEvent)
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	// 2.模型回退警告
	if msg := a.ModelFallbackMessage(); msg != "" {
		fmt.Fprintf(stderr, "Rivo: %s\n", msg)
	}
	for _, warning := range a.Warnings() {
		fmt.Fprintf(stderr, "Rivo: warning: %s\n", warning)
	}

	// 3.开始交互模式
	return runREPL(stdin, stdout, stderr, func(ctx context.Context, input string) error {
		_, err := a.Run(ctx, input)
		if err == nil {
			fmt.Fprintln(stdout)
		}
		return err
	}, func(ctx context.Context, instructions string) error {
		return a.Compact(ctx, instructions)
	}, func() ([]session.SessionInfo, error) {
		return session.ListSessions(a.Cwd())
	}, func(path string) error {
		na, err := app.Open(path, cfg, onEvent)
		if err != nil {
			return err
		}
		a = na
		if msg := a.ModelFallbackMessage(); msg != "" {
			fmt.Fprintf(stderr, "Rivo: %s\n", msg)
		}
		for _, warning := range a.Warnings() {
			fmt.Fprintf(stderr, "Rivo: warning: %s\n", warning)
		}
		return nil
	}, func() {
		a.Reload()
	})
}

// resumeRecentApp 恢复最近会话，无会话时静默新建
func resumeRecentApp(cfg app.Config, onEvent llm.AgentEventSink) (*app.App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}
	infos, err := session.ListSessions(cwd)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return app.New(cfg, onEvent)
	}
	return app.Open(infos[0].Path, cfg, onEvent)
}

type targetKind uint8

const (
	targetNew targetKind = iota
	targetRecent
	targetPath
	targetID
)

type sessionTarget struct {
	kind targetKind
	path string
	id   string
}

func openSessionTarget(cfg app.Config, target sessionTarget, onEvent llm.AgentEventSink) (*app.App, error) {
	switch target.kind {
	case targetRecent:
		return resumeRecentApp(cfg, onEvent)
	case targetPath:
		return app.Open(target.path, cfg, onEvent)
	case targetID:
		return app.NewWithSessionID(cfg, target.id, onEvent)
	default:
		return app.New(cfg, onEvent)
	}
}

func resolveSessionTarget(cwd, sessionArg, sessionID string, continueRecent bool) (sessionTarget, error) {
	if sessionArg != "" {
		if strings.ContainsAny(sessionArg, `/\\`) || strings.HasSuffix(sessionArg, ".jsonl") {
			path := sessionArg
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			path = filepath.Clean(path)
			return sessionTarget{kind: targetPath, path: path}, nil
		}
		local, err := session.ListSessions(cwd)
		if err != nil {
			return sessionTarget{}, err
		}
		if match := matchSession(local, sessionArg); match != nil {
			return sessionTarget{kind: targetPath, path: match.Path}, nil
		}
		all, err := session.ListAllSessions()
		if err != nil {
			return sessionTarget{}, err
		}
		if match := matchSession(all, sessionArg); match != nil {
			return sessionTarget{kind: targetPath, path: match.Path}, nil
		}
		return sessionTarget{}, fmt.Errorf("no session found matching %q", sessionArg)
	}
	if sessionID != "" {
		local, err := session.ListSessions(cwd)
		if err != nil {
			return sessionTarget{}, err
		}
		for _, info := range local {
			if info.ID == sessionID {
				return sessionTarget{kind: targetPath, path: info.Path}, nil
			}
		}
		return sessionTarget{kind: targetID, id: sessionID}, nil
	}
	if continueRecent {
		return sessionTarget{kind: targetRecent}, nil
	}
	return sessionTarget{kind: targetNew}, nil
}

func matchSession(infos []session.SessionInfo, value string) *session.SessionInfo {
	for i := range infos {
		if infos[i].ID == value {
			return &infos[i]
		}
	}
	for i := range infos {
		if strings.HasPrefix(infos[i].ID, value) {
			return &infos[i]
		}
	}
	return nil
}

func runREPL(stdin io.Reader, stdout, stderr io.Writer, run func(context.Context, string) error, compact func(context.Context, string) error, listSessions func() ([]session.SessionInfo, error), resume func(string) error, reload func()) int {
	reader := bufio.NewReader(stdin)
	for {
		if _, err := fmt.Fprint(stdout, "> "); err != nil {
			return 1
		}

		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(stderr, "Rivo: failed to read input: %v\n", err)
			return 1
		}
		if line == "" {
			return 0
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		if input == "/exit" || input == "/quit" {
			return 0
		}
		// /compact [instructions] 手动触发上下文压缩
		if input == "/compact" || strings.HasPrefix(input, "/compact ") {
			instructions := strings.TrimSpace(strings.TrimPrefix(input, "/compact"))
			if err := compact(context.Background(), instructions); err != nil {
				fmt.Fprintf(stderr, "Rivo: %v\n", err)
			}
			continue
		}
		// /resume 列出会话并按编号恢复
		if input == "/resume" {
			if !handleResume(reader, stdout, stderr, listSessions, resume) {
				return 1
			}
			continue
		}
		// /reload 重读 AGENTS.md 与 skills，变更在下次请求前生效
		if input == "/reload" {
			reload()
			fmt.Fprintln(stdout, "[reloaded context files and skills]")
			continue
		}
		if err := run(context.Background(), input); err != nil {
			fmt.Fprintf(stderr, "Rivo: %v\n", err)
		}
	}
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// 1.先扫描 --help：一旦出现即优先展示帮助，不校验其余参数
	for i := 0; i < len(args); i++ {
		if args[i] == "--help" || args[i] == "-h" {
			fmt.Fprint(stdout, helpText)
			return 0
		}
		switch args[i] {
		case "--cost", "--provider", "--model", "--thinking":
			i++
		}
	}
	// 2.正式解析：位置参数既可作为 TUI/REPL 初始 prompt，也可用于 --print
	var (
		continueRecent bool
		printRequested bool
		sessionArg     string
		sessionID      string
		costPath       string
		providerArg    string
		modelArg       string
		thinkingArg    string
		positional     []string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--cost":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				fmt.Fprintf(stderr, "Rivo: flag --cost requires a value\n")
				return 2
			}
			if costPath != "" {
				fmt.Fprintln(stderr, "Rivo: flag --cost may only be specified once")
				return 2
			}
			i++
			costPath = args[i]
		case "--continue", "--resume":
			continueRecent = true
		case "--session", "--session-id":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				fmt.Fprintf(stderr, "Rivo: flag %s requires a value\n", arg)
				return 2
			}
			i++
			if arg == "--session" {
				sessionArg = args[i]
			} else {
				sessionID = args[i]
			}
		case "--print", "-p":
			printRequested = true
		case "--provider", "--model", "--thinking":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				fmt.Fprintf(stderr, "Rivo: flag %s requires a value\n", arg)
				return 2
			}
			i++
			switch arg {
			case "--provider":
				providerArg = args[i]
			case "--model":
				modelArg = args[i]
			case "--thinking":
				thinkingArg = args[i]
			}
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(stderr, "Rivo: unsupported argument %q\n", arg)
				fmt.Fprintln(stderr, "Run 'Rivo --help' for usage.")
				return 2
			}
			positional = append(positional, arg)
		}
	}
	if costPath != "" {
		if continueRecent || printRequested || sessionArg != "" || sessionID != "" || providerArg != "" || modelArg != "" || thinkingArg != "" || len(positional) > 0 {
			fmt.Fprintln(stderr, "Rivo: --cost cannot be combined with runtime options or a prompt")
			return 2
		}
		return costMode(costPath, stdout, stderr)
	}
	if sessionID != "" {
		if sessionArg != "" || continueRecent {
			fmt.Fprintln(stderr, "Rivo: --session-id cannot be combined with --session, --continue, or --resume")
			return 2
		}
		if err := session.ValidateID(sessionID); err != nil {
			fmt.Fprintf(stderr, "Rivo: invalid --session-id %q: %v\n", sessionID, err)
			return 2
		}
	}

	if len(positional) > 0 && strings.HasPrefix(positional[0], "-") {
		fmt.Fprintf(stderr, "Rivo: unsupported argument %q\n", positional[0])
		fmt.Fprintln(stderr, "Run 'Rivo --help' for usage.")
		return 2
	}

	// 2.加载运行配置并应用 CLI 覆盖
	cfg, err := app.Load()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	for _, warning := range cfg.Warnings {
		fmt.Fprintf(stderr, "Rivo: warning: %s\n", warning)
	}
	if err := app.ApplyOverrides(&cfg, providerArg, modelArg, thinkingArg); err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: failed to get working directory: %v\n", err)
		return 2
	}
	target, err := resolveSessionTarget(cwd, sessionArg, sessionID, continueRecent)
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	if target.kind == targetID {
		fmt.Fprintf(stderr, "Rivo: warning: no project session found with id %q; creating a new session with that id\n", target.id)
	}

	// 3.print 模式：单轮执行后退出
	if printRequested {
		prompt, err := buildPrintPrompt(positional, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "Rivo: %v\n", err)
			return 2
		}
		return printModeTarget(cfg, prompt, target, stdout, stderr)
	}
	var initialPrompt string
	if len(positional) > 0 {
		initialPrompt = strings.Join(positional, " ")
	}
	// 终端交互使用全屏 TUI，管道/测试流保留逐行入口
	if terminalStreams(stdin, stdout) {
		return runTUI(cfg, target, initialPrompt, stdin.(*os.File), stdout.(*os.File), stderr)
	}
	if initialPrompt != "" {
		return printModeTarget(cfg, initialPrompt, target, stdout, stderr)
	}
	return replTarget(cfg, target, stdin, stdout, stderr)
}

// costMode 读取指定 JSONL 会话并输出其累计 token 与美元费用
func costMode(path string, stdout, stderr io.Writer) int {
	if data, err := os.ReadFile(path); err != nil {
		fmt.Fprintf(stderr, "Rivo: failed to read session file: %v\n", err)
		return 2
	} else if strings.TrimSpace(string(data)) == "" {
		printCostTotals(session.UsageTotals{}, stdout)
		return 0
	}
	sm, err := session.Open(path, "")
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	printCostTotals(sm.UsageTotals(), stdout)
	return 0
}

func printCostTotals(totals session.UsageTotals, stdout io.Writer) {
	fmt.Fprintf(stdout, "Cost (USD): $%.6f\n", totals.Cost.Total)
	fmt.Fprintf(stdout, "Input tokens: %d\n", totals.InputTokens)
	fmt.Fprintf(stdout, "Output tokens: %d\n", totals.OutputTokens)
	fmt.Fprintf(stdout, "Cache read tokens: %d\n", totals.CacheReadTokens)
	fmt.Fprintf(stdout, "Cache write tokens: %d\n", totals.CacheWriteTokens)
	fmt.Fprintf(stdout, "Total tokens: %d\n", totals.TotalTokens)
	fmt.Fprintf(stdout, "Unknown cost entries: %d\n", totals.UnknownCostEntries)
}

func terminalStreams(stdin io.Reader, stdout io.Writer) bool {
	in, inOK := stdin.(*os.File)
	out, outOK := stdout.(*os.File)
	if !inOK || !outOK {
		return false
	}
	return term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

// buildPrintPrompt 组合 print 模式的提示词：位置参数优先，无位置参数时从 stdin 读取
func buildPrintPrompt(positional []string, stdin io.Reader) (string, error) {
	if len(positional) > 0 {
		return strings.Join(positional, " "), nil
	}
	if f, ok := stdin.(*os.File); ok {
		if stat, err := f.Stat(); err == nil && stat.Mode()&os.ModeCharDevice != 0 {
			return "", fmt.Errorf("no prompt provided (use arguments or pipe stdin)")
		}
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("failed to read stdin: %w", err)
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", fmt.Errorf("no prompt provided (use arguments or pipe stdin)")
	}
	return prompt, nil
}

// promptRunner 抽象单轮执行，便于 print 模式测试
type promptRunner interface {
	Run(ctx context.Context, input string) (*llm.AssistantMessage, error)
}

// printMode 非交互执行一轮，运行错误或 error/aborted 收场时打到 stderr 并返回 1
func printMode(cfg app.Config, prompt string, resumeRecent bool, stdout, stderr io.Writer, runner ...promptRunner) int {
	var run promptRunner
	if len(runner) > 0 {
		run = runner[0]
	} else if resumeRecent {
		a, err := resumeRecentApp(cfg, nil)
		if err != nil {
			fmt.Fprintf(stderr, "Rivo: %v\n", err)
			return 1
		}
		run = a
	} else {
		a, err := app.New(cfg, nil)
		if err != nil {
			fmt.Fprintf(stderr, "Rivo: %v\n", err)
			return 1
		}
		run = a
	}
	msg, runErr := run.Run(context.Background(), prompt)
	if runErr != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", runErr)
		return 1
	}
	if msg.StopReason == llm.StopReasonError {
		fmt.Fprintf(stderr, "Rivo: request %s\n", msg.StopReason)
		return 1
	}
	if msg.StopReason == llm.StopReasonAborted {
		// 取消不是故障：输出已收到的部分内容，退出码 130 对齐 Ctrl+C 惯例
		for _, content := range msg.Content {
			if text, ok := content.(llm.TextContent); ok && text.Text != "" {
				fmt.Fprintln(stdout, text.Text)
			}
		}
		return 130
	}
	for _, content := range msg.Content {
		if text, ok := content.(llm.TextContent); ok {
			fmt.Fprintln(stdout, text.Text)
		}
	}
	return 0
}

func printModeTarget(cfg app.Config, prompt string, target sessionTarget, stdout, stderr io.Writer) int {
	a, err := openSessionTarget(cfg, target, nil)
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 1
	}
	return printMode(cfg, prompt, false, stdout, stderr, a)
}

// handleResume 打印会话列表并读取编号完成恢复，返回 false 表示输入读取失败
func handleResume(reader *bufio.Reader, stdout, stderr io.Writer, listSessions func() ([]session.SessionInfo, error), resume func(string) error) bool {
	infos, err := listSessions()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return true
	}
	if len(infos) == 0 {
		fmt.Fprintln(stdout, "no sessions to resume")
		return true
	}
	// 1.按编号打印列表
	for i, info := range infos {
		fmt.Fprintf(stdout, "%d. %s  %s\n", i+1, relativeTime(info.ModTime), info.Summary)
	}
	fmt.Fprint(stdout, "Enter a number to resume, press Enter to cancel: ")
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(stderr, "Rivo: failed to read input: %v\n", err)
		return false
	}
	// 2.非法编号与回车都视为取消
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(infos) {
		return true
	}
	if err := resume(infos[n-1].Path); err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
	}
	return true
}

func relativeTime(t time.Time) string { return session.RelativeTime(t) }
