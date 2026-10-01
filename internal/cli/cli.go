package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
)

const helpText = `Rivo - AI coding assistant

Usage:
  Rivo [options]

Options:
  -h, --help  Show this help
`

func Repl(stdin io.Reader, stdout, stderr io.Writer) int {
	// 1.获取到配置信息
	cfg, err := app.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}

	// 2.组装Agent
	a, err := app.New(cfg, func(ev llm.AgentEvent) error {
		if update, ok := ev.(llm.MessageUpdateEvent); ok && update.AssistantMessageEvent.Type == llm.EventTextDelta {
			_, err := fmt.Fprint(stdout, update.AssistantMessageEvent.Delta)
			return err
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}

	// 3.开始交互模式
	return runREPL(stdin, stdout, stderr, func(ctx context.Context, input string) error {
		_, err := a.Run(ctx, input)
		if err == nil {
			fmt.Fprintln(stdout)
		}
		return err
	})
}

func runREPL(stdin io.Reader, stdout, stderr io.Writer, run func(context.Context, string) error) int {
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
		if err := run(context.Background(), input); err != nil {
			fmt.Fprintf(stderr, "Rivo: %v\n", err)
		}
	}
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return Repl(stdin, stdout, stderr)
	}

	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			continue
		}

		fmt.Fprintf(stderr, "Rivo: 不支持的参数 %q\n", arg)
		fmt.Fprintln(stderr, "请运行 'Rivo --help' 查看用法。")
		return 2
	}

	fmt.Fprint(stdout, helpText)
	return 0
}
