package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestRunDelegatesToCLI(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--help"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if stdout.Len() == 0 {
		t.Error("run() stdout is empty")
	}
	if stderr.Len() != 0 {
		t.Errorf("run() stderr = %q, want empty", stderr.String())
	}
}

// TestRivoHelperProcess 在子进程中调用 main，验证真实退出码
func TestRivoHelperProcess(t *testing.T) {
	if os.Getenv("Rivo_HELPER_PROCESS") != "1" {
		return
	}
	// 丢弃测试框架参数，使 main 接收无参数调用
	os.Args = []string{"Rivo"}
	main()
}

func TestMainWithoutArgumentsExitsWithDiagnostic(t *testing.T) {
	t.Parallel()

	cmd := exec.Command(os.Args[0], "-test.run=^TestRivoHelperProcess$")
	cmd.Env = append(os.Environ(), "Rivo_HELPER_PROCESS=1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Rivo process error = %v, want exit code 2", err)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Fatalf("Rivo process exit code = %d, want 2", got)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("Rivo process stdout = %q, want empty", got)
	}
	if got := stderr.String(); got != "Rivo: 交互模式尚未实现\n" {
		t.Errorf("Rivo process stderr = %q, want unimplemented diagnostic", got)
	}
}
