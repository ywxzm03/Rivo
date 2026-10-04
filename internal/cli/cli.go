package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

const helpText = `Rivo - AI coding assistant

Usage:
  Rivo [options]

Options:
  -h, --help    Show this help
      --continue  Continue the most recent session
      --resume    Alias of --continue
`

func Repl(stdin io.Reader, stdout, stderr io.Writer) int {
	return repl(false, stdin, stdout, stderr)
}

// repl 组装 Agent 并进入交互模式；continueRecent 为 true 时恢复最近会话，无会话则静默新建
func repl(continueRecent bool, stdin io.Reader, stdout, stderr io.Writer) int {
	// 1.获取到配置信息
	cfg, err := app.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}

	onEvent := func(ev llm.AgentEvent) error {
		if update, ok := ev.(llm.MessageUpdateEvent); ok {
			if text, ok := update.AssistantMessageEvent.(llm.AssistantMessageTextDeltaEvent); ok {
				_, err := fmt.Fprint(stdout, text.Delta)
				return err
			}
		}
		// 压缩完成打印一行反馈，覆盖自动与溢出来源；手动压缩的失败经 runREPL
		// 的错误路径打印，避免同一错误输出两次
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
			// 按 rune 截断，避免把多字节字符从中间切开输出乱码
			if runes := []rune(preview); len(runes) > 80 {
				preview = string(runes[:80]) + "..."
			}
			if _, err := fmt.Fprintf(stdout, "\n[context compacted (%s): %d -> %d tokens]\n%s\n", end.Reason, end.TokensBefore, end.TokensAfter, preview); err != nil {
				return err
			}
		}
		return nil
	}

	// 2.组装 Agent：恢复最近会话或新建
	var a *app.App
	if continueRecent {
		a, err = resumeRecentApp(cfg, onEvent)
	} else {
		a, err = app.New(cfg, onEvent)
	}
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	// 3.模型回退警告：恢复继续，但明确告知实际使用的模型
	if msg := a.ModelFallbackMessage(); msg != "" {
		fmt.Fprintf(stderr, "Rivo: %s\n", msg)
	}

	// 4.开始交互模式
	return runREPL(stdin, stdout, stderr, func(ctx context.Context, input string) error {
		_, err := a.Run(ctx, input)
		if err == nil {
			fmt.Fprintln(stdout)
		}
		return err
	}, func(ctx context.Context, instructions string) error {
		// 经闭包取当前 a，保证 /resume 替换实例后压缩作用于新会话
		return a.Compact(ctx, instructions)
	}, func() ([]session.SessionInfo, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return session.ListSessions(cwd)
	}, func(path string) error {
		na, err := app.Open(path, cfg, onEvent)
		if err != nil {
			return err
		}
		a = na
		if msg := a.ModelFallbackMessage(); msg != "" {
			fmt.Fprintf(stderr, "Rivo: %s\n", msg)
		}
		return nil
	})
}

// resumeRecentApp 恢复最近会话；目录下无会话时静默新建（照搬 pi --continue）
func resumeRecentApp(cfg app.Config, onEvent llm.AgentEventSink) (*app.App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("获取工作目录失败: %w", err)
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

func runREPL(stdin io.Reader, stdout, stderr io.Writer, run func(context.Context, string) error, compact func(context.Context, string) error, listSessions func() ([]session.SessionInfo, error), resume func(string) error) int {
	reader := bufio.NewReader(stdin)
	for {
		if _, err := fmt.Fprint(stdout, "> "); err != nil {
			return 1
		}

		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(stderr, "Rivo: 读取输入失败: %v\n", err)
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
		// /resume 列出会话并按编号恢复；空列表或取消留在当前会话
		if input == "/resume" {
			if !handleResume(reader, stdout, stderr, listSessions, resume) {
				return 1
			}
			continue
		}
		if err := run(context.Background(), input); err != nil {
			fmt.Fprintf(stderr, "Rivo: %v\n", err)
		}
	}
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// 1.先扫描 --help：一旦出现即优先展示帮助，不再校验其余参数
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(stdout, helpText)
			return 0
		}
	}
	// 2.解析恢复参数：--continue 与 --resume 本阶段同路径，恢复最近会话
	continueRecent := false
	for _, arg := range args {
		switch arg {
		case "--continue", "--resume":
			continueRecent = true
		default:
			fmt.Fprintf(stderr, "Rivo: 不支持的参数 %q\n", arg)
			fmt.Fprintln(stderr, "请运行 'Rivo --help' 查看用法。")
			return 2
		}
	}
	return repl(continueRecent, stdin, stdout, stderr)
}

// handleResume 打印会话列表并读取编号完成恢复；返回 false 表示输入读取失败需退出 REPL。
// 空列表、非法编号、直接回车都不改变当前会话
func handleResume(reader *bufio.Reader, stdout, stderr io.Writer, listSessions func() ([]session.SessionInfo, error), resume func(string) error) bool {
	infos, err := listSessions()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return true
	}
	if len(infos) == 0 {
		fmt.Fprintln(stdout, "没有可恢复的会话")
		return true
	}
	// 1.按编号打印列表：编号 + 相对时间 + 首条用户消息摘要
	for i, info := range infos {
		fmt.Fprintf(stdout, "%d. %s  %s\n", i+1, relativeTime(info.ModTime), info.Summary)
	}
	fmt.Fprint(stdout, "输入编号恢复，回车取消: ")
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(stderr, "Rivo: 读取输入失败: %v\n", err)
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

// relativeTime 把修改时间格式化为中文相对时间；未来时间与时钟回拨场景退回日期，超过一个月也退回日期
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 0:
		return t.Format("2006-01-02")
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
