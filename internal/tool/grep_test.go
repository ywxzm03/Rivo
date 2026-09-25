package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func writeFakeRipgrep(t *testing.T, output string, exitCode int, stderr string) (argsPath string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "rg")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RG_ARGS_FILE\"\nif [ -n \"$RG_STDERR\" ]; then printf '%s' \"$RG_STDERR\" >&2; fi\nprintf '%s' \"$RG_OUTPUT\"\nexit \"$RG_EXIT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	argsPath = filepath.Join(dir, "args")
	t.Setenv("PATH", dir)
	t.Setenv("RG_ARGS_FILE", argsPath)
	t.Setenv("RG_OUTPUT", output)
	t.Setenv("RG_EXIT", strconv.Itoa(exitCode))
	t.Setenv("RG_STDERR", stderr)
	return argsPath
}

func execGrep(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	content, err := (&Grep{}).Execute(context.Background(), args)
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

func matchEvent(path string, line int, text string) string {
	return fmt.Sprintf(`{"type":"match","data":{"path":{"text":%q},"line_number":%d,"lines":{"text":%q}}}`, path, line, text+"\n")
}

func contextEvent(path string, line int, text string) string {
	return fmt.Sprintf(`{"type":"context","data":{"path":{"text":%q},"line_number":%d,"lines":{"text":%q}}}`, path, line, text+"\n")
}

func TestGrepBasicAndArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("before\nmatch line\nafter"), 0o600); err != nil {
		t.Fatal(err)
	}
	argsPath := writeFakeRipgrep(t, matchEvent(path, 2, "match line"), 0, "")

	out, err := execGrep(t, map[string]any{
		"pattern":    "needle",
		"path":       dir,
		"glob":       "*.go",
		"ignoreCase": true,
		"literal":    true,
		"context":    float64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "f.txt:2: match line") {
		t.Errorf("output = %q, want match", out)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	want := []string{"--json", "--line-number", "--color=never", "--hidden", "--ignore-case", "--fixed-strings", "--glob", "*.go", "--", "needle", dir}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args = %#v, want %#v", got, want)
	}
}

func TestGrepProtectsPatternFromArguments(t *testing.T) {
	dir := t.TempDir()
	argsPath := writeFakeRipgrep(t, matchEvent(filepath.Join(dir, "f.txt"), 1, "x"), 0, "")
	if _, err := execGrep(t, map[string]any{"pattern": "--pre=/tmp/payload", "path": dir}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	values := strings.Split(strings.TrimSpace(string(args)), "\n")
	for i, value := range values {
		if value == "--pre=/tmp/payload" && (i == 0 || values[i-1] != "--") {
			t.Fatalf("pattern was not after --: %#v", values)
		}
	}
}

func TestGrepNoMatchesAndErrors(t *testing.T) {
	dir := t.TempDir()
	writeFakeRipgrep(t, "", 1, "")
	out, err := execGrep(t, map[string]any{"pattern": "none", "path": dir})
	if err != nil || out != "No matches found" {
		t.Errorf("no matches = %q, %v", out, err)
	}

	writeFakeRipgrep(t, "", 2, "bad pattern")
	_, err = execGrep(t, map[string]any{"pattern": "[", "path": dir})
	if err == nil || err.Error() != "bad pattern" {
		t.Errorf("error = %v, want bad pattern", err)
	}
}

func TestGrepLimitContextAndLongLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	long := strings.Repeat("x", 600)
	output := strings.Join([]string{
		matchEvent(path, 2, long),
		matchEvent(path, 4, "second"),
		matchEvent(path, 6, "third"),
	}, "\n")
	writeFakeRipgrep(t, output, 0, "")
	out, err := execGrep(t, map[string]any{"pattern": "x", "path": dir, "limit": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "third") || !strings.Contains(out, "second") {
		t.Errorf("limited output = %q", out)
	}
	if !strings.Contains(out, "2 matches limit reached. Use limit=4 for more, or refine pattern") {
		t.Errorf("missing limit note: %q", out)
	}
	if !strings.Contains(out, "Some lines truncated to 500 chars. Use read tool to see full lines") {
		t.Errorf("missing line note: %q", out)
	}

	if err := os.WriteFile(path, []byte("before\nmatch\nafter\nend"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeRipgrep(t, strings.Join([]string{
		matchEvent(path, 2, "match"),
		matchEvent(path, 3, "after"),
	}, "\n"), 0, "")
	out, err = execGrep(t, map[string]any{"pattern": "x", "path": dir, "context": float64(1), "limit": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"f.txt-1- before", "f.txt:2: match", "f.txt-3- after", "f.txt-2- match", "f.txt:3: after", "f.txt-4- end"} {
		if !strings.Contains(out, want) {
			t.Errorf("context output = %q, missing %q", out, want)
		}
	}
	if strings.Count(out, "f.txt-2- match") != 1 {
		t.Errorf("overlapping context was not repeated as pi does: %q", out)
	}
}

func TestGrepNonPositiveLimitUsesOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("one\ntwo"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeRipgrep(t, strings.Join([]string{
		matchEvent(path, 1, "one"),
		matchEvent(path, 2, "two"),
	}, "\n"), 0, "")
	out, err := execGrep(t, map[string]any{"pattern": "x", "path": dir, "limit": float64(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "f.txt:1: one") || strings.Contains(out, "f.txt:2:") || !strings.Contains(out, "1 matches limit reached") {
		t.Errorf("limit=0 output = %q, want pi's effective limit of 1", out)
	}
}

func TestGrepContextReadFailureUsesPlaceholder(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.txt")
	writeFakeRipgrep(t, matchEvent(missing, 1, "match"), 0, "")
	out, err := execGrep(t, map[string]any{"pattern": "x", "path": dir, "context": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "missing.txt:1: (unable to read file)" {
		t.Errorf("output = %q, want unreadable-file placeholder", out)
	}
}

func TestGrepContextTwoLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nmatch\nfour\nfive\nsix"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeRipgrep(t, matchEvent(path, 3, "match"), 0, "")
	out, err := execGrep(t, map[string]any{"pattern": "x", "path": dir, "context": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	want := "f.txt-1- one\nf.txt-2- two\nf.txt:3: match\nf.txt-4- four\nf.txt-5- five"
	if !strings.HasPrefix(out, want) {
		t.Errorf("output = %q, want context=2 block %q", out, want)
	}
}

func TestGrepCombinesPiTruncationNotices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	rows := make([]string, 121)
	for i := range rows {
		rows[i] = strings.Repeat("x", 600)
	}
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeRipgrep(t, matchEvent(path, 61, rows[60]), 0, "")
	out, err := execGrep(t, map[string]any{
		"pattern": "x",
		"path":    dir,
		"context": float64(60),
		"limit":   float64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, notice := range []string{
		"1 matches limit reached. Use limit=2 for more, or refine pattern",
		"50.0KB limit reached",
		"Some lines truncated to 500 chars. Use read tool to see full lines",
	} {
		if !strings.Contains(out, notice) {
			t.Errorf("output is missing notice %q", notice)
		}
	}
}

func TestGrepKillsProcessAtMatchLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("match"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "continued")
	script := filepath.Join(t.TempDir(), "rg")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$RG_EVENT\"\nsleep 1\ntouch %q\n", marker)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(script))
	t.Setenv("RG_EVENT", matchEvent(path, 1, "match"))
	if _, err := execGrep(t, map[string]any{"pattern": "x", "path": dir, "limit": float64(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker exists or stat failed (%v): rg continued after reaching limit", err)
	}
}

func TestGrepSingleFilePathAndDefinition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeRipgrep(t, matchEvent(path, 1, "x"), 0, "")
	out, err := execGrep(t, map[string]any{"pattern": "x", "path": path})
	if err != nil {
		t.Fatal(err)
	}
	if out != "f.txt:1: x" {
		t.Errorf("output = %q, want file basename", out)
	}

	g := &Grep{}
	if g.Name() != "grep" {
		t.Errorf("Name() = %q", g.Name())
	}
	for _, want := range []string{"100 matches", "50KB", "500 chars", "Respects .gitignore"} {
		if !strings.Contains(g.Description(), want) {
			t.Errorf("description missing %q", want)
		}
	}
	params := g.Parameters()
	if got := params["required"]; fmt.Sprint(got) != "[pattern]" {
		t.Errorf("required = %v, want pattern", got)
	}
	properties := params["properties"].(map[string]any)
	for _, key := range []string{"pattern", "path", "glob", "ignoreCase", "literal", "context", "limit"} {
		if _, ok := properties[key]; !ok {
			t.Errorf("missing parameter %q", key)
		}
	}
}
