package main

import (
	"io"
	"os"

	"github.com/ywxzm03/Rivo/internal/cli"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return cli.Run(args, stdin, stdout, stderr)
}
