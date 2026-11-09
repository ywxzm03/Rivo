package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestFileToolsUseCurrentAgentWorkingDirectory(t *testing.T) {
	parent, child := t.TempDir(), t.TempDir()
	for _, dir := range []string{parent, child} {
		if err := os.WriteFile(filepath.Join(dir, "value.txt"), []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := llm.WithWorkingDir(context.Background(), child)
	write := &Write{WorkingDir: parent}
	if _, err := write.Execute(ctx, map[string]any{"path": "value.txt", "content": "written"}); err != nil {
		t.Fatal(err)
	}
	edit := &Edit{WorkingDir: parent}
	if _, err := edit.Execute(ctx, map[string]any{"path": "value.txt", "edits": []any{map[string]any{"oldText": "written", "newText": "edited"}}}); err != nil {
		t.Fatal(err)
	}
	read := &Read{WorkingDir: parent}
	result, err := read.Execute(ctx, map[string]any{"path": "value.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if llm.TextOf(result.Content) != "edited" {
		t.Fatalf("read used configured parent directory: %q", llm.TextOf(result.Content))
	}
	for dir, want := range map[string]string{parent: "before", child: "edited"} {
		data, err := os.ReadFile(filepath.Join(dir, "value.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q want %q", dir, data, want)
		}
	}
	if _, err := write.Execute(context.Background(), map[string]any{"path": "value.txt", "content": "fallback"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(parent, "value.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fallback" {
		t.Fatalf("configured directory fallback lost: %q", data)
	}
}

func TestForegroundShellUsesCurrentAgentWorkingDirectory(t *testing.T) {
	parent, child := t.TempDir(), t.TempDir()
	for dir, value := range map[string]string{parent: "parent", child: "child"} {
		if err := os.WriteFile(filepath.Join(dir, "cwd-marker.txt"), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := llm.WithWorkingDir(context.Background(), child)
	result, err := (&Bash{WorkingDir: parent}).Execute(ctx, map[string]any{"command": "cat cwd-marker.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(llm.TextOf(result.Content)); got != "child" {
		t.Fatalf("foreground shell read %q, want current agent marker", got)
	}
}
