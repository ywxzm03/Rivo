package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunREPL(t *testing.T) {
	var inputs []string
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runREPL(strings.NewReader("\n hello \n/quit\n"), &stdout, &stderr, func(_ context.Context, input string) error {
		inputs = append(inputs, input)
		return nil
	})

	if code != 0 {
		t.Fatalf("runREPL() code = %d, want 0", code)
	}
	if got := strings.Join(inputs, ","); got != "hello" {
		t.Errorf("runREPL() inputs = %q, want %q", got, "hello")
	}
	if got := stdout.String(); got != "> > > " {
		t.Errorf("runREPL() stdout = %q, want %q", got, "> > > ")
	}
	if got := stderr.String(); got != "" {
		t.Errorf("runREPL() stderr = %q, want empty", got)
	}
}

func TestRunREPLLineBeyondScannerLimit(t *testing.T) {
	long := strings.Repeat("x", 64*1024+1)

	var inputs []string
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runREPL(strings.NewReader(long+"\n/exit\n"), &stdout, &stderr, func(_ context.Context, input string) error {
		inputs = append(inputs, input)
		return nil
	})

	if code != 0 {
		t.Fatalf("runREPL() code = %d, want 0", code)
	}
	if len(inputs) != 1 {
		t.Fatalf("runREPL() received %d inputs, want 1", len(inputs))
	}
	if inputs[0] != long {
		t.Errorf("runREPL() input length = %d, want %d", len(inputs[0]), len(long))
	}
	if got := stderr.String(); got != "" {
		t.Errorf("runREPL() stderr = %q, want empty", got)
	}
}

func TestRunREPLContinuesAfterAgentError(t *testing.T) {
	var inputs []string
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	sentinel := errors.New("boom")

	code := runREPL(strings.NewReader("hello\n/exit\n"), &stdout, &stderr, func(_ context.Context, input string) error {
		inputs = append(inputs, input)
		return sentinel
	})

	if code != 0 {
		t.Fatalf("runREPL() code = %d, want 0", code)
	}
	if got := strings.Join(inputs, ","); got != "hello" {
		t.Errorf("runREPL() inputs = %q, want %q", got, "hello")
	}
	if got := stdout.String(); got != "> > " {
		t.Errorf("runREPL() stdout = %q, want %q", got, "> > ")
	}
	if got := stderr.String(); got != "Rivo: boom\n" {
		t.Errorf("runREPL() stderr = %q, want %q", got, "Rivo: boom\n")
	}
}

func TestRunREPLEOF(t *testing.T) {
	var inputs []string
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runREPL(strings.NewReader("hello"), &stdout, &stderr, func(_ context.Context, input string) error {
		inputs = append(inputs, input)
		return nil
	})

	if code != 0 {
		t.Fatalf("runREPL() code = %d, want 0", code)
	}
	if got := strings.Join(inputs, ","); got != "hello" {
		t.Errorf("runREPL() inputs = %q, want %q", got, "hello")
	}
	if got := stdout.String(); got != "> > " {
		t.Errorf("runREPL() stdout = %q, want %q", got, "> > ")
	}
	if got := stderr.String(); got != "" {
		t.Errorf("runREPL() stderr = %q, want empty", got)
	}
}
