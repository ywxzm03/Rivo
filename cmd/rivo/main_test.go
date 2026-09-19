package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRunDelegatesToCLI(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--help"}, strings.NewReader(""), &stdout, &stderr)

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

func TestRivoHelperProcess(t *testing.T) {
	if os.Getenv("Rivo_HELPER_PROCESS") != "1" {
		return
	}

	os.Args = []string{"Rivo", "--help"}
	main()
}

func TestRivoProcessHelpExitsSuccessfully(t *testing.T) {
	t.Parallel()

	cmd := exec.Command(os.Args[0], "-test.run=^TestRivoHelperProcess$")
	cmd.Env = append(os.Environ(), "Rivo_HELPER_PROCESS=1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("Rivo process exit code = %d, want 0", exitErr.ExitCode())
		}
		t.Fatalf("Rivo process error = %v", err)
	}
	if got := stdout.String(); got == "" {
		t.Error("Rivo process stdout is empty")
	}
	if got := stderr.String(); got != "" {
		t.Errorf("Rivo process stderr = %q, want empty", got)
	}
}
