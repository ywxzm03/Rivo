package gitmeta

import (
	"os"
	"path/filepath"
	"testing"
)

// benchRepo 创建最小仓库布局
func benchRepo(b *testing.B, head string) string {
	b.Helper()
	root := b.TempDir()
	dir := filepath.Join(root, ".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte(head), 0o644); err != nil {
		b.Fatal(err)
	}
	return root
}

func BenchmarkCurrentBranch(b *testing.B) {
	root := benchRepo(b, "ref: refs/heads/main\n")
	b.ResetTimer()
	for b.Loop() {
		_ = CurrentBranch(root)
	}
}
