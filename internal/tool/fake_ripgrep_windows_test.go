//go:build windows

package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

const fakeRipgrepProgram = `package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	argsPath := os.Getenv("RG_ARGS_FILE")
	if argsPath != "" {
		file, err := os.Create(argsPath)
		if err != nil {
			panic(err)
		}
		for _, arg := range os.Args[1:] {
			fmt.Fprintln(file, arg)
		}
		if err := file.Close(); err != nil {
			panic(err)
		}
	}
	if stderr := os.Getenv("RG_STDERR"); stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	fmt.Fprint(os.Stdout, os.Getenv("RG_OUTPUT"))
	if sleepSeconds := os.Getenv("RG_SLEEP_SECONDS"); sleepSeconds != "" {
		seconds, _ := strconv.ParseFloat(sleepSeconds, 64)
		time.Sleep(time.Duration(seconds * float64(time.Second)))
	}
	if marker := os.Getenv("RG_MARKER"); marker != "" {
		if err := os.WriteFile(marker, nil, 0600); err != nil {
			panic(err)
		}
	}
	exitCode, _ := strconv.Atoi(os.Getenv("RG_EXIT"))
	os.Exit(exitCode)
}
`

func writeFakeRipgrep(t *testing.T, output string, exitCode int, stderr string) (argsPath string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(fakeRipgrepProgram), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "rg.exe")
	goExecutable := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	command := exec.Command(goExecutable, "build", "-o", executable, source)
	command.Env = append(os.Environ(), "GO111MODULE=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake ripgrep: %v\n%s", err, output)
	}
	return configureFakeRipgrep(t, dir, filepath.Join(dir, "args"), output, exitCode, stderr)
}

func configureFakeRipgrep(t *testing.T, dir, argsPath, output string, exitCode int, stderr string) string {
	t.Helper()
	t.Setenv("PATH", dir)
	t.Setenv("RG_ARGS_FILE", argsPath)
	t.Setenv("RG_OUTPUT", output)
	t.Setenv("RG_EXIT", strconv.Itoa(exitCode))
	t.Setenv("RG_STDERR", stderr)
	t.Setenv("RG_SLEEP_SECONDS", "")
	t.Setenv("RG_MARKER", "")
	return argsPath
}
