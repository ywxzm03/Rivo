package prompt

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

func TestDiscoverContextFilesGlobalThenAncestors(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeFile(t, filepath.Join(agentDir, "AGENTS.md"), "global")
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "inner")
	writeFile(t, filepath.Join(outer, "AGENTS.md"), "outer")
	writeFile(t, filepath.Join(inner, "AGENTS.md"), "inner")

	files := discoverContextFiles(inner, agentDir)
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3: %+v", len(files), files)
	}
	// 全局优先，随后从最外层祖先到 cwd
	want := []string{"global", "outer", "inner"}
	for i, w := range want {
		if files[i].Content != w {
			t.Errorf("files[%d].Content = %q, want %q", i, files[i].Content, w)
		}
	}
}

func TestDiscoverContextFilesPrefersFirstCandidateInDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.MD"), "upper")
	// AGENTS.md 在候选表中靠前，同目录优先
	writeFile(t, filepath.Join(root, "AGENTS.md"), "preferred")

	files := discoverContextFiles(root, filepath.Join(root, "agent"))
	if len(files) != 1 || files[0].Content != "preferred" {
		t.Fatalf("files = %+v, want AGENTS.md preferred", files)
	}
}

func TestDiscoverContextFilesSkipsDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := discoverContextFiles(root, filepath.Join(root, "agent"))
	if len(files) != 0 {
		t.Fatalf("files = %+v, want directory candidate skipped", files)
	}
}

func TestDiscoverContextFilesNoFiles(t *testing.T) {
	root := t.TempDir()
	if files := discoverContextFiles(root, filepath.Join(root, "agent")); len(files) != 0 {
		t.Fatalf("files = %+v, want none", files)
	}
}

func TestDiscoverContextFilesStripsBOM(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "\ufeffcontent")
	files := discoverContextFiles(root, filepath.Join(root, "agent"))
	if len(files) != 1 || files[0].Content != "content" {
		t.Fatalf("files = %+v, want BOM stripped", files)
	}
}

// TestFindShadowedContextFileOrdinaryRepo 普通仓库下 worktreeRoot 与主仓相同，不产生遮蔽
func TestFindShadowedContextFileOrdinaryRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "main")
	if got := findShadowedContextFile(root); got != "" {
		t.Errorf("shadowed = %q, want none in ordinary repo", got)
	}
}

// TestFindShadowedContextFileWorktree 主仓为 worktree 的祖先时，worktree 根的同名文件遮蔽主仓副本
func TestFindShadowedContextFileWorktree(t *testing.T) {
	root := t.TempDir()
	mainRepo := filepath.Join(root, "main")
	worktree := filepath.Join(mainRepo, "wt")
	commonGitDir := filepath.Join(mainRepo, ".git")

	// 主仓：.git 是目录，commondir 指向自身，满足「主仓根下 .git == commonGitDir」
	writeFile(t, filepath.Join(commonGitDir, "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(mainRepo, "AGENTS.md"), "main repo")
	// worktree：.git 是文件，指向主仓 git 目录下的 worktrees 条目
	wtGitDir := filepath.Join(commonGitDir, "worktrees", "wt")
	writeFile(t, filepath.Join(wtGitDir, "HEAD"), "ref: refs/heads/feat\n")
	writeFile(t, filepath.Join(wtGitDir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+wtGitDir+"\n")
	writeFile(t, filepath.Join(worktree, "AGENTS.md"), "worktree")

	got := findShadowedContextFile(worktree)
	// 返回值与 PI 一致地经过 realpath 归一，断言也归一后比较
	want := canonicalizePath(filepath.Join(mainRepo, "AGENTS.md"))
	if got != want {
		t.Fatalf("shadowed = %q, want %q", got, want)
	}

	// 遮蔽生效后，主仓副本被丢弃，只保留 worktree 自己的
	files := discoverContextFiles(worktree, filepath.Join(root, "agent"))
	for _, f := range files {
		if f.Content == "main repo" {
			t.Errorf("files = %+v, want main repo copy shadowed", files)
		}
	}
}

// TestFindShadowedContextFileSiblingWorktree sibling worktree 的主仓不是其祖先，不遮蔽
func TestFindShadowedContextFileSiblingWorktree(t *testing.T) {
	root := t.TempDir()
	mainRepo := filepath.Join(root, "main")
	commonGitDir := filepath.Join(mainRepo, ".git")
	writeFile(t, filepath.Join(commonGitDir, "HEAD"), "ref: refs/heads/main\n")

	sibling := filepath.Join(root, "sibling")
	wtGitDir := filepath.Join(commonGitDir, "worktrees", "sib")
	writeFile(t, filepath.Join(wtGitDir, "HEAD"), "ref: refs/heads/feat\n")
	writeFile(t, filepath.Join(wtGitDir, "commondir"), "../../..\n")
	writeFile(t, filepath.Join(sibling, ".git"), "gitdir: "+wtGitDir+"\n")
	writeFile(t, filepath.Join(sibling, "AGENTS.md"), "sibling")

	if got := findShadowedContextFile(sibling); got != "" {
		t.Errorf("shadowed = %q, want none for sibling worktree", got)
	}
}

// TestFindShadowedContextFileBareLayout 裸仓布局下主仓根不是有效检出，不遮蔽
func TestFindShadowedContextFileBareLayout(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, ".bare")
	worktree := filepath.Join(root, "main")
	writeFile(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/main\n")
	// 裸仓布局：主仓根下没有 .git 目录，因此不满足遮蔽条件
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+bare+"\n")
	writeFile(t, filepath.Join(worktree, "AGENTS.md"), "container")

	if got := findShadowedContextFile(worktree); got != "" {
		t.Errorf("shadowed = %q, want none for bare layout", got)
	}
}

// TestFindShadowedContextFileSubmodule submodule 的 gitdir 位于 .git/modules 下，无 commondir，不遮蔽
func TestFindShadowedContextFileSubmodule(t *testing.T) {
	root := t.TempDir()
	superproject := filepath.Join(root, "super")
	writeFile(t, filepath.Join(superproject, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(superproject, "AGENTS.md"), "super")

	sub := filepath.Join(superproject, "sub")
	subGitDir := filepath.Join(superproject, ".git", "modules", "sub")
	writeFile(t, filepath.Join(subGitDir, "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(sub, ".git"), "gitdir: "+subGitDir+"\n")
	writeFile(t, filepath.Join(sub, "AGENTS.md"), "submodule")

	if got := findShadowedContextFile(sub); got != "" {
		t.Errorf("shadowed = %q, want none for submodule", got)
	}
	// submodule 不遮蔽 superproject 的上下文文件
	files := discoverContextFiles(sub, filepath.Join(root, "agent"))
	found := false
	for _, f := range files {
		if f.Content == "super" {
			found = true
		}
	}
	if !found {
		t.Errorf("files = %+v, want superproject context preserved", files)
	}
}

// TestFindShadowedContextFileNoCandidateInWorktree worktree 根没有候选文件时不遮蔽主仓副本
func TestFindShadowedContextFileNoCandidateInWorktree(t *testing.T) {
	root := t.TempDir()
	mainRepo := filepath.Join(root, "main")
	commonGitDir := filepath.Join(mainRepo, ".git")
	writeFile(t, filepath.Join(commonGitDir, "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(mainRepo, "AGENTS.md"), "main repo")

	worktree := filepath.Join(mainRepo, "wt")
	wtGitDir := filepath.Join(commonGitDir, "worktrees", "wt")
	writeFile(t, filepath.Join(wtGitDir, "HEAD"), "ref: refs/heads/feat\n")
	writeFile(t, filepath.Join(wtGitDir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+wtGitDir+"\n")

	if got := findShadowedContextFile(worktree); got != "" {
		t.Errorf("shadowed = %q, want none when worktree has no candidate", got)
	}
	// 未遮蔽时主仓副本仍应被继承
	files := discoverContextFiles(worktree, filepath.Join(root, "agent"))
	found := false
	for _, f := range files {
		if f.Content == "main repo" {
			found = true
		}
	}
	if !found {
		t.Errorf("files = %+v, want main repo context inherited", files)
	}
}

// TestFindShadowedContextFileAboveMainRepo 主仓之上的无关目录保留
func TestFindShadowedContextFileAboveMainRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "above")
	mainRepo := filepath.Join(root, "main")
	commonGitDir := filepath.Join(mainRepo, ".git")
	writeFile(t, filepath.Join(commonGitDir, "HEAD"), "ref: refs/heads/main\n")

	worktree := filepath.Join(mainRepo, "wt")
	wtGitDir := filepath.Join(commonGitDir, "worktrees", "wt")
	writeFile(t, filepath.Join(wtGitDir, "HEAD"), "ref: refs/heads/feat\n")
	writeFile(t, filepath.Join(wtGitDir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+wtGitDir+"\n")
	writeFile(t, filepath.Join(worktree, "AGENTS.md"), "worktree")

	files := discoverContextFiles(worktree, filepath.Join(root, "agent"))
	found := false
	for _, f := range files {
		if f.Content == "above" {
			found = true
		}
	}
	if !found {
		t.Errorf("files = %+v, want unrelated ancestor context preserved", files)
	}
}

func TestLoaderReloadReplacesResults(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "first")

	loader := NewLoader(root, agentDir)
	loader.Reload()
	if len(loader.ContextFiles()) != 1 || loader.ContextFiles()[0].Content != "first" {
		t.Fatalf("files = %+v, want first content", loader.ContextFiles())
	}

	writeFile(t, filepath.Join(root, "AGENTS.md"), "second")
	loader.Reload()
	if len(loader.ContextFiles()) != 1 || loader.ContextFiles()[0].Content != "second" {
		t.Fatalf("files = %+v, want reloaded content", loader.ContextFiles())
	}
}
