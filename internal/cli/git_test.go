package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeGitHead 创建最小仓库布局并写入 HEAD
func writeGitHead(t *testing.T, root, head string) {
	t.Helper()
	dir := filepath.Join(root, ".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitBranchReadsHeadWithoutSubprocess(t *testing.T) {
	root := t.TempDir()
	writeGitHead(t, root, "ref: refs/heads/feature-x\n")
	if got := gitBranch(root); got != "feature-x" {
		t.Fatalf("gitBranch() = %q, want feature-x", got)
	}
}

func TestGitBranchReadsWorktreeHead(t *testing.T) {
	main := t.TempDir()
	writeGitHead(t, main, "ref: refs/heads/main\n")
	gitDir := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/worktree-branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(worktree); got != "worktree-branch" {
		t.Fatalf("gitBranch(worktree) = %q, want worktree-branch", got)
	}
}

func TestGitBranchDetachedHeadIsEmpty(t *testing.T) {
	root := t.TempDir()
	writeGitHead(t, root, "0123456789abcdef0123456789abcdef01234567\n")
	if got := gitBranch(root); got != "" {
		t.Fatalf("gitBranch(detached) = %q, want empty", got)
	}
}

func TestGitBranchHonorsGitDirEnv(t *testing.T) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/from-env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	if got := gitBranch(t.TempDir()); got != "from-env" {
		t.Fatalf("gitBranch(GIT_DIR) = %q, want from-env", got)
	}
}
