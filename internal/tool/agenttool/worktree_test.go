package agenttool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateWorktreeRequiresGitAndUsesDetachedHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	parent := t.TempDir()
	sessionDir := t.TempDir()
	// 1.没有提交历史的仓库无法创建 worktree,错误必须可读
	if _, _, err := createSubagentWorktree(parent, sessionDir, "agent-1"); err == nil {
		t.Fatal("expected an error outside a git repository")
	}
	initRepo(t, parent)
	path, base, err := createSubagentWorktree(parent, sessionDir, "agent-2")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exec.Command("git", "-C", parent, "worktree", "remove", "--force", path).Run() }()
	// 2.worktree 目录必须落在 sessionDir/subagents 下,且处于 detached 状态
	if want := filepath.Join(sessionDir, "subagents", "worktree-agent-2"); path != want {
		t.Fatalf("worktree path = %q, want %q", path, want)
	}
	output, err := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "HEAD" {
		t.Fatalf("worktree is not detached: %q", strings.TrimSpace(string(output)))
	}
	// 3.返回的基线 commit 必须就是 worktree 的 HEAD,清理时靠它发现新提交
	head, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if base != strings.TrimSpace(string(head)) {
		t.Fatalf("base commit = %q, want worktree HEAD %q", base, strings.TrimSpace(string(head)))
	}
}

func TestCleanupWorktreeKeepsDirtyTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	parent := t.TempDir()
	sessionDir := t.TempDir()
	initRepo(t, parent)
	path, base, err := createSubagentWorktree(parent, sessionDir, "agent-3")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exec.Command("git", "-C", parent, "worktree", "remove", "--force", path).Run() }()
	metadata := metadataPathFor(path, sessionDir, "agent-3")
	if err := os.WriteFile(metadata, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 1.干净 worktree 应被回收,元数据一并删除
	if !cleanupSubagentWorktree(path, parent, base, metadata) {
		t.Fatal("clean worktree was not cleaned up")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if _, err := os.Stat(metadata); !os.IsNotExist(err) {
		t.Fatalf("metadata still exists: %v", err)
	}
	// 2.有未提交改动时必须保留,不能丢用户工作
	path, base, err = createSubagentWorktree(parent, sessionDir, "agent-4")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cleanupSubagentWorktree(path, parent, base, metadataPathFor(path, sessionDir, "agent-4")) {
		t.Fatal("dirty worktree should be preserved")
	}
	if _, err := os.Stat(filepath.Join(path, "dirty.txt")); err != nil {
		t.Fatalf("dirty worktree lost user changes: %v", err)
	}
}

func TestCleanupWorktreeKeepsUnmergedCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	parent := t.TempDir()
	sessionDir := t.TempDir()
	initRepo(t, parent)
	path, base, err := createSubagentWorktree(parent, sessionDir, "agent-5")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exec.Command("git", "-C", parent, "worktree", "remove", "--force", path).Run() }()
	// 1.工作树干净,但子 agent 在 worktree 里提交了新 commit:同样视为脏
	if err := os.WriteFile(filepath.Join(path, "committed.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, path, "subagent commit")
	metadata := metadataPathFor(path, sessionDir, "agent-5")
	if err := os.WriteFile(metadata, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cleanupSubagentWorktree(path, parent, base, metadata) {
		t.Fatal("worktree with unmerged commits should be preserved")
	}
	if _, err := os.Stat(filepath.Join(path, "committed.txt")); err != nil {
		t.Fatalf("worktree with commits was removed: %v", err)
	}
	if _, err := os.Stat(metadata); err != nil {
		t.Fatalf("metadata should survive: %v", err)
	}
	// 2.基线未知时 fail-closed,不能冒险删掉可能含提交的 worktree
	if cleanupSubagentWorktree(path, parent, "", metadata) {
		t.Fatal("worktree without a known base commit should be preserved")
	}
}

func TestMetadataPathFor(t *testing.T) {
	got := metadataPathFor("/wt", "/sessions", "agent-9")
	if want := filepath.Join("/sessions", "subagents", "agent-agent-9.meta.json"); got != want {
		t.Fatalf("metadata path = %q, want %q", got, want)
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "seed.txt")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
}

// commitAll 把 dir 下的全部改动提交,用于构造 worktree 内的未合并 commit
func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", message)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
