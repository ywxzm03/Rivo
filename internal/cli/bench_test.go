package cli

import (
	"os"
	"testing"
)

// BenchmarkGitBranch 覆盖仓库内与非仓库两种分支解析路径
func BenchmarkGitBranch(b *testing.B) {
	repo, err := os.Getwd()
	if err != nil {
		b.Fatal(err)
	}
	nonRepo := b.TempDir()
	b.Run("repo", func(b *testing.B) {
		for b.Loop() {
			_ = gitBranch(repo)
		}
	})
	b.Run("nonrepo", func(b *testing.B) {
		for b.Loop() {
			_ = gitBranch(nonRepo)
		}
	})
}
