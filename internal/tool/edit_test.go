package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func runEdit(t *testing.T, path string, edits any, extra map[string]any) (llm.ToolResult, error) {
	t.Helper()
	args := map[string]any{"path": path, "edits": edits}
	for key, value := range extra {
		args[key] = value
	}
	return (&Edit{}).Execute(context.Background(), args)
}

func TestEditAppliesMultipleEditsAndReturnsDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runEdit(t, path, []any{
		map[string]any{"oldText": "three", "newText": "THREE"},
		map[string]any{"oldText": "one", "newText": "ONE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ONE\ntwo\nTHREE\nfour\n" {
		t.Fatalf("file = %q", got)
	}
	details, ok := result.Details.(EditDetails)
	if !ok {
		t.Fatalf("Details type = %T, want EditDetails", result.Details)
	}
	if details.FirstChangedLine == nil || *details.FirstChangedLine != 1 {
		t.Errorf("FirstChangedLine = %v, want 1", details.FirstChangedLine)
	}
	if !strings.Contains(details.Diff, "-1 one") || !strings.Contains(details.Diff, "+1 ONE") {
		t.Errorf("Diff = %q, want numbered replacement", details.Diff)
	}
	if !strings.Contains(details.Patch, "--- "+path) || !strings.Contains(details.Patch, "+ONE") {
		t.Errorf("Patch = %q, want unified patch", details.Patch)
	}
	if len(result.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(result.Content))
	}
}

func TestEditPatchIncludesFourLinesOfContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	content := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runEdit(t, path, []any{map[string]any{"oldText": "seven", "newText": "SEVEN"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	details := result.Details.(EditDetails)
	for _, line := range []string{" three\n", " four\n", " five\n", " six\n", " eight\n", " nine\n", " ten\n", " eleven\n"} {
		if !strings.Contains(details.Patch, line) {
			t.Errorf("patch missing context %q: %q", strings.TrimSpace(line), details.Patch)
		}
	}
}

func TestEditSupportsPiInputForms(t *testing.T) {
	tests := []struct {
		name  string
		edits any
		extra map[string]any
	}{
		{name: "single object", edits: map[string]any{"oldText": "a", "newText": "b"}},
		{name: "JSON array", edits: `[{"oldText":"a","newText":"b"}]`},
		{name: "JSON object", edits: `{"oldText":"a","newText":"b"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.txt")
			if err := os.WriteFile(path, []byte("a\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := runEdit(t, path, test.edits, test.extra); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "b\n" {
				t.Errorf("file = %q, want %q", got, "b\n")
			}
		})
	}
}

func TestEditFuzzyMatchesAndPreservesUnchangedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	original := "  keep  \n‘quoted’  \nlast  \n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runEdit(t, path, []any{map[string]any{
		"oldText": "'quoted'\n",
		"newText": "plain\n",
	}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "  keep  \nplain\nlast  \n" {
		t.Errorf("file = %q", got)
	}
}

func TestEditPreservesBOMAndCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	original := append([]byte{0xef, 0xbb, 0xbf}, []byte("first\r\nsecond\r\n")...)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runEdit(t, path, []any{map[string]any{"oldText": "second", "newText": "changed"}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xef, 0xbb, 0xbf}, []byte("first\r\nchanged\r\n")...)
	if string(got) != string(want) {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestEditFailureIsAtomic(t *testing.T) {
	tests := []struct {
		name  string
		edits []any
	}{
		{name: "missing", edits: []any{map[string]any{"oldText": "missing", "newText": "x"}}},
		{name: "duplicate", edits: []any{map[string]any{"oldText": "same", "newText": "x"}}},
		{name: "overlap", edits: []any{
			map[string]any{"oldText": "abc", "newText": "x"},
			map[string]any{"oldText": "bc", "newText": "y"},
		}},
		{name: "no-op", edits: []any{map[string]any{"oldText": "same", "newText": "same"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.txt")
			const original = "same same\nabc\n"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := runEdit(t, path, test.edits, nil); err == nil {
				t.Fatal("expected an error")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Errorf("file changed after failed edit: %q", got)
			}
		})
	}
}

func TestEditRejectsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	if _, err := runEdit(t, path, []any{map[string]any{"oldText": "a", "newText": "b"}}, nil); err == nil {
		t.Fatal("editing a missing file returned nil error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing target was created or stat failed: %v", err)
	}
}

func TestResolveEditPathPiForms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a b.txt")
	for _, input := range []string{"@" + path, "file://" + path, strings.ReplaceAll(path, " ", "\u00a0")} {
		got, err := resolveEditPathFrom(input, "")
		if err != nil {
			t.Fatal(err)
		}
		if got != path {
			t.Errorf("resolveEditPathFrom(%q) = %q, want %q", input, got, path)
		}
	}
}
