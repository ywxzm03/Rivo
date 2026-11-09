package gitmeta

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile 写入测试文件并创建父目录
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initRepo 创建一个只含 HEAD 的最小仓库，足以覆盖分支解析
func initRepo(t *testing.T, root, head string) string {
	t.Helper()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), head)
	return root
}

func TestFindNormalRepoFromNestedDir(t *testing.T) {
	root := initRepo(t, t.TempDir(), "ref: refs/heads/main\n")
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := Find(nested)
	if repo == nil {
		t.Fatal("Find() = nil, want repo")
	}
	if repo.Root != root || repo.GitDir != filepath.Join(root, ".git") || repo.CommonDir != filepath.Join(root, ".git") {
		t.Fatalf("Find() = %+v, want root/gitdir under %q", repo, root)
	}
	if got := repo.Branch(); got != "main" {
		t.Fatalf("Branch() = %q, want main", got)
	}
}

func TestFindWorktreeGitFile(t *testing.T) {
	main := initRepo(t, t.TempDir(), "ref: refs/heads/main\n")
	gitDir := filepath.Join(main, ".git", "worktrees", "feature")
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/feature\n")
	writeFile(t, filepath.Join(gitDir, "commondir"), "../..\n")

	worktree := t.TempDir()
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+gitDir+"\n")

	repo := Find(worktree)
	if repo == nil {
		t.Fatal("Find() = nil, want worktree repo")
	}
	if repo.Root != worktree {
		t.Errorf("Root = %q, want %q", repo.Root, worktree)
	}
	if repo.GitDir != gitDir {
		t.Errorf("GitDir = %q, want %q", repo.GitDir, gitDir)
	}
	if repo.CommonDir != filepath.Join(main, ".git") {
		t.Errorf("CommonDir = %q, want main .git", repo.CommonDir)
	}
	if got := repo.Branch(); got != "feature" {
		t.Fatalf("Branch() = %q, want feature", got)
	}
}

func TestFindRelativeGitDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "actual", "HEAD"), "ref: refs/heads/relative\n")
	writeFile(t, filepath.Join(root, ".git"), "gitdir: actual\n")

	repo := Find(root)
	if repo == nil {
		t.Fatal("Find() = nil, want repo")
	}
	if repo.GitDir != filepath.Join(root, "actual") {
		t.Fatalf("GitDir = %q, want relative gitdir resolved", repo.GitDir)
	}
	if got := repo.Branch(); got != "relative" {
		t.Fatalf("Branch() = %q, want relative", got)
	}
}

func TestFindRejectsMissingHead(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if repo := Find(root); repo != nil {
		t.Fatalf("Find() = %+v, want nil when HEAD is missing", repo)
	}
}

func TestFindRejectsMalformedGitFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git"), "not a gitdir pointer\n")
	if repo := Find(root); repo != nil {
		t.Fatalf("Find() = %+v, want nil for malformed .git file", repo)
	}
}

func TestFindReturnsNilOutsideRepo(t *testing.T) {
	if repo := Find(t.TempDir()); repo != nil {
		t.Fatalf("Find() = %+v, want nil outside repo", repo)
	}
}

func TestBranchDetachedHead(t *testing.T) {
	root := initRepo(t, t.TempDir(), "0123456789abcdef0123456789abcdef01234567\n")
	repo := Find(root)
	if repo == nil {
		t.Fatal("Find() = nil, want repo")
	}
	if got := repo.Branch(); got != "" {
		t.Fatalf("Branch() = %q, want empty for detached HEAD", got)
	}
}

func TestBranchUnbornBranch(t *testing.T) {
	root := initRepo(t, t.TempDir(), "ref: refs/heads/master\n")
	if got := CurrentBranch(root); got != "master" {
		t.Fatalf("CurrentBranch() = %q, want master", got)
	}
}

func TestNilRepoBranchIsEmpty(t *testing.T) {
	var repo *Repo
	if got := repo.Branch(); got != "" {
		t.Fatalf("nil Branch() = %q, want empty", got)
	}
}

func TestCurrentBranchOutsideRepo(t *testing.T) {
	if got := CurrentBranch(t.TempDir()); got != "" {
		t.Fatalf("CurrentBranch() = %q, want empty", got)
	}
}

func TestFindBareRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "HEAD"), "ref: refs/heads/bare\n")
	for _, dir := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := Find(nested)
	if repo == nil {
		t.Fatal("Find() = nil, want bare repo")
	}
	if repo.Root != root || repo.GitDir != root || repo.CommonDir != root {
		t.Fatalf("Find() = %+v, want bare repo root", repo)
	}
	if got := repo.Branch(); got != "bare" {
		t.Fatalf("Branch() = %q, want bare", got)
	}
}
