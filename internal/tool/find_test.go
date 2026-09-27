package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func execFind(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	content, err := (&Find{}).Execute(context.Background(), args)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, item := range content.Content {
		if text, ok := item.(llm.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String(), nil
}

func TestFindBasicAndArguments(t *testing.T) {
	dir := t.TempDir()
	argsPath := writeFakeRipgrep(t, "src/main.go\n.hidden/config.json", 0, "")
	out, err := execFind(t, map[string]any{"pattern": "*.go", "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if out != "src/main.go\n.hidden/config.json" {
		t.Fatalf("output = %q", out)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	want := []string{"--files", "--hidden", "--color=never", "--path-separator=/", "--glob", "**/*.go", "--", "."}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args = %#v, want %#v", got, want)
	}
	if !strings.Contains((&Find{}).Description(), "respects .gitignore") {
		t.Errorf("description = %q, want ignore behavior", (&Find{}).Description())
	}
}

func TestFindSlashGlobAndArgumentSafety(t *testing.T) {
	if got := buildFindRipgrepArgs("src/**/*.go"); strings.Join(got, "\x00") != strings.Join([]string{
		"--files", "--hidden", "--color=never", "--path-separator=/", "--glob", "src/**/*.go", "--", ".",
	}, "\x00") {
		t.Errorf("slash glob args = %#v", got)
	}
	if got := buildFindRipgrepArgs("--pre=/tmp/payload"); strings.Join(got, "\x00") != strings.Join([]string{
		"--files", "--hidden", "--color=never", "--path-separator=/", "--glob", "--pre=/tmp/payload", "--", ".",
	}, "\x00") {
		t.Errorf("glob argument = %#v", got)
	}
}

func TestFindNoResultsAndErrors(t *testing.T) {
	dir := t.TempDir()
	writeFakeRipgrep(t, "", 1, "")
	out, err := execFind(t, map[string]any{"pattern": "none", "path": dir})
	if err != nil || out != "No files found" {
		t.Errorf("no results = %q, %v", out, err)
	}

	writeFakeRipgrep(t, "", 2, "bad glob")
	_, err = execFind(t, map[string]any{"pattern": "[", "path": dir})
	if err == nil || err.Error() != "bad glob" {
		t.Errorf("error = %v, want bad glob", err)
	}
}

func TestFindLimitAndNonPositiveLimit(t *testing.T) {
	dir := t.TempDir()
	writeFakeRipgrep(t, "a.go\nb.go\nc.go\n", 0, "")
	out, err := execFind(t, map[string]any{"pattern": "*.go", "path": dir, "limit": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "a.go\nb.go\n\n[2 paths limit reached. Use limit=4 to see more]" {
		t.Errorf("limited output = %q", out)
	}

	writeFakeRipgrep(t, "a.go\nb.go\n", 0, "")
	out, err = execFind(t, map[string]any{"pattern": "*.go", "path": dir, "limit": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "limit reached") {
		t.Errorf("exact-limit output falsely reports truncation: %q", out)
	}

	writeFakeRipgrep(t, "a.go\nb.go\n", 0, "")
	out, err = execFind(t, map[string]any{"pattern": "*.go", "path": dir, "limit": float64(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "a.go") || strings.Contains(out, "b.go") || !strings.Contains(out, "1 paths limit reached") {
		t.Errorf("non-positive limit output = %q", out)
	}
}

func TestFindByteLimit(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 600)
	for i := range paths {
		paths[i] = fmt.Sprintf("%03d-%s.go", i, strings.Repeat("x", 100))
	}
	writeFakeRipgrep(t, strings.Join(paths, "\n"), 0, "")
	out, err := execFind(t, map[string]any{"pattern": "*.go", "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "50.0KB limit reached") {
		t.Errorf("output missing byte-limit notice: %q", out[len(out)-100:])
	}
	if len(out) > defaultMaxBytes+100 {
		t.Errorf("output size = %d, exceeds expected cap", len(out))
	}
}

func TestFindPathValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := execFind(t, map[string]any{"pattern": "*", "path": filepath.Join(dir, "missing")}); err == nil || !strings.Contains(err.Error(), "Path not found") {
		t.Errorf("missing path error = %v", err)
	}
	if _, err := execFind(t, map[string]any{"pattern": "*", "path": file}); err == nil || !strings.Contains(err.Error(), "Not a directory") {
		t.Errorf("file path error = %v", err)
	}
	if _, err := execFind(t, map[string]any{}); err == nil || !strings.Contains(err.Error(), "missing required parameter: pattern") {
		t.Errorf("missing pattern error = %v", err)
	}
}

func TestFindKillsProcessAfterLimitProbe(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "continued")
	script := filepath.Join(t.TempDir(), "rg")
	body := fmt.Sprintf("#!/bin/sh\nprintf 'a.go\\nb.go\\n'\n/bin/sleep 1\ntouch %q\n", marker)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(script))
	if _, err := execFind(t, map[string]any{"pattern": "*.go", "path": dir, "limit": float64(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker exists or stat failed (%v): rg continued after limit probe", err)
	}
}

func TestFindDefinition(t *testing.T) {
	f := &Find{}
	if f.Name() != "find" {
		t.Errorf("Name() = %q", f.Name())
	}
	params := f.Parameters()
	if got := fmt.Sprint(params["required"]); got != "[pattern]" {
		t.Errorf("required = %s, want pattern", got)
	}
	properties := params["properties"].(map[string]any)
	for _, key := range []string{"pattern", "path", "limit"} {
		if _, ok := properties[key]; !ok {
			t.Errorf("missing parameter %q", key)
		}
	}
}
