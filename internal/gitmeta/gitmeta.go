// Package gitmeta 读取仓库目录布局与当前分支，不启动 git 子进程
package gitmeta

import (
	"os"
	"path/filepath"
	"strings"
)

// Repo 描述 cwd 所属仓库的目录布局
type Repo struct {
	Root      string // 工作树根目录，即包含 .git 的目录
	GitDir    string // 当前工作树或子模块的 git 目录，含 HEAD
	CommonDir string // 共享 git 目录；普通仓库与 GitDir 相同，worktree 下指向主仓 .git
}

// Find 从 cwd 向上查找 .git；目录形态直接使用，文件形态（worktree/submodule）解析 gitdir 与 commondir
// 找不到仓库或 HEAD 不可用时返回 nil
func Find(cwd string) *Repo {
	dir := cwd
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err != nil && isGitDir(dir) {
			// 裸仓库没有 .git，HEAD/objects/refs 直接位于仓库根
			return &Repo{Root: dir, GitDir: dir, CommonDir: dir}
		} else if err == nil {
			if info.IsDir() {
				if !exists(filepath.Join(gitPath, "HEAD")) {
					return nil
				}
				return &Repo{Root: dir, GitDir: gitPath, CommonDir: gitPath}
			}
			content, err := os.ReadFile(gitPath)
			if err != nil {
				return nil
			}
			trimmed := strings.TrimSpace(string(content))
			if !strings.HasPrefix(trimmed, "gitdir: ") {
				return nil
			}
			gitDir := strings.TrimSpace(strings.TrimPrefix(trimmed, "gitdir: "))
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(dir, gitDir)
			}
			if !exists(filepath.Join(gitDir, "HEAD")) {
				return nil
			}
			commonDir := gitDir
			if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
				common := strings.TrimSpace(string(data))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
				commonDir = common
			}
			return &Repo{Root: dir, GitDir: gitDir, CommonDir: commonDir}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// Branch 读取 GitDir/HEAD；不是 ref: refs/heads/ 形式时返回空串，与 git branch --show-current 对齐
func (r *Repo) Branch() string {
	if r == nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(r.GitDir, "HEAD"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	const prefix = "ref: refs/heads/"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	return strings.TrimPrefix(line, prefix)
}

// CurrentBranch 是 Find 与 Branch 的组合，找不到仓库时返回空串
func CurrentBranch(cwd string) string {
	return Find(cwd).Branch()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isGitDir 判断目录本身是否为 git 目录（裸仓库）
func isGitDir(dir string) bool {
	return exists(filepath.Join(dir, "HEAD")) &&
		exists(filepath.Join(dir, "objects")) &&
		exists(filepath.Join(dir, "refs"))
}
