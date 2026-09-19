package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ywxzm03/Rivo/internal/cli"
)

func main() {
	var stdin io.Reader = os.Stdin
	var stdout, stderr io.Writer = os.Stdout, os.Stderr

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGINT)

	go func() {
		for {
			<-sigCh

			go func() {
				fmt.Fprintln(stdout, "再执行一次 Ctrl + C 则会退出")
				fmt.Fprint(stdout, ">")
			}()

			select {
			case <-sigCh:
				os.Exit(130)
			case <-time.After(time.Second * 2):
				continue
			}
		}
	}()

	os.Exit(run(os.Args[1:], stdin, stdout, stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return cli.Run(args, stdin, stdout, stderr)
}
