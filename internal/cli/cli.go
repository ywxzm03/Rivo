package cli

import (
	"fmt"
	"io"
)

const helpText = `Rivo - AI coding assistant

Usage:
  Rivo [options]

Options:
  -h, --help  Show this help
`

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Rivo: 交互模式尚未实现")
		return 2
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
