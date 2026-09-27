package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func execWrite(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	content, err := (&Write{}).Execute(context.Background(), args)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, item := range content {
		if text, ok := item.(llm.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String(), nil
}

func TestWriteCreatesAndOverwritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "file.txt")

	out, err := execWrite(t, map[string]any{"path": path, "content": "first"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Successfully wrote to "+path {
		t.Errorf("output = %q", out)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first" {
		t.Errorf("initial content = %q", content)
	}

	if _, err := execWrite(t, map[string]any{"path": path, "content": "second"}); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "second" {
		t.Errorf("overwritten content = %q", content)
	}
}

func TestWriteSupportsEmptyAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if _, err := execWrite(t, map[string]any{"path": "relative/empty.txt", "content": ""}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "relative", "empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Errorf("empty content length = %d", len(content))
	}
}

func TestWriteSupportsTildePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if _, err := execWrite(t, map[string]any{"path": "~/tilde.txt", "content": "home"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "tilde.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "home" {
		t.Errorf("content = %q", content)
	}
}

func TestWriteHonorsCanceledContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "canceled.txt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	content, err := (&Write{}).Execute(ctx, map[string]any{"path": path, "content": "content"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if content != nil {
		t.Fatalf("content = %#v, want nil", content)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("parent directory exists or stat failed: %v", err)
	}
}

func TestWriteValidatesArgumentsAndFilesystemErrors(t *testing.T) {
	for _, args := range []map[string]any{
		{},
		{"path": "file.txt"},
		{"path": "file.txt", "content": 1},
		{"path": 1, "content": "content"},
	} {
		if _, err := execWrite(t, args); err == nil {
			t.Errorf("args %#v: error = nil", args)
		}
	}

	dir := t.TempDir()
	parent := filepath.Join(dir, "parent")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := execWrite(t, map[string]any{
		"path":    filepath.Join(parent, "child.txt"),
		"content": "content",
	})
	if err == nil {
		t.Fatal("writing below a file returned nil error")
	}
}

func TestWriteDefinition(t *testing.T) {
	w := &Write{}
	if w.Name() != "write" {
		t.Errorf("Name() = %q", w.Name())
	}
	if !strings.Contains(w.Description(), "overwrites") || !strings.Contains(w.Description(), "parent directories") {
		t.Errorf("Description() = %q", w.Description())
	}
	params := w.Parameters()
	if got := fmt.Sprint(params["required"]); got != "[path content]" {
		t.Errorf("required = %s", got)
	}
	properties := params["properties"].(map[string]any)
	for _, key := range []string{"path", "content"} {
		if _, ok := properties[key]; !ok {
			t.Errorf("missing parameter %q", key)
		}
	}
}
