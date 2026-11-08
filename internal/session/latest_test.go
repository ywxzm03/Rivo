package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touchSession 写入空会话文件并设定修改时间
func touchSession(t *testing.T, dir, name string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLatestSessionPathMissingDir(t *testing.T) {
	path, err := latestSessionPathInDir(filepath.Join(t.TempDir(), "not-exist"))
	if err != nil {
		t.Fatalf("latestSessionPathInDir() error = %v, want nil", err)
	}
	if path != "" {
		t.Fatalf("path = %q, want empty", path)
	}
}

func TestLatestSessionPathIgnoresNonJSONLAndDirectories(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	touchSession(t, dir, "old.jsonl", base)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := latestSessionPathInDir(dir)
	if err != nil {
		t.Fatalf("latestSessionPathInDir() error = %v", err)
	}
	if path != filepath.Join(dir, "old.jsonl") {
		t.Fatalf("path = %q, want only jsonl file", path)
	}
}

func TestLatestSessionPathPicksNewestModTime(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	touchSession(t, dir, "a.jsonl", base.Add(-time.Hour))
	want := touchSession(t, dir, "b.jsonl", base)
	touchSession(t, dir, "c.jsonl", base.Add(-2*time.Hour))

	path, err := latestSessionPathInDir(dir)
	if err != nil {
		t.Fatalf("latestSessionPathInDir() error = %v", err)
	}
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestLatestSessionPathBreaksModTimeTiesByPath(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	touchSession(t, dir, "b.jsonl", base)
	want := touchSession(t, dir, "a.jsonl", base)

	path, err := latestSessionPathInDir(dir)
	if err != nil {
		t.Fatalf("latestSessionPathInDir() error = %v", err)
	}
	if path != want {
		t.Fatalf("path = %q, want lexicographically first on tie %q", path, want)
	}
}

func TestLatestSessionPathIncludesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	want := touchSession(t, dir, "empty.jsonl", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	path, err := latestSessionPathInDir(dir)
	if err != nil {
		t.Fatalf("latestSessionPathInDir() error = %v", err)
	}
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}
