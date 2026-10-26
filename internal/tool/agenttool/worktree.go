package agenttool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// createSubagentWorktree 为任务创建 detached worktree,优先以 origin/HEAD 为基线。
// 同时返回 worktree 的起始 commit,清理时用它发现子 agent 新产生的提交
func createSubagentWorktree(parentCwd, sessionDir, id string) (string, string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", "", fmt.Errorf("subagent: git is unavailable for worktree: %w", err)
	}
	path := filepath.Join(sessionDir, "subagents", "worktree-"+id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	// 1.有远端默认分支时以它为准,否则退回本地 HEAD
	base := "HEAD"
	if output, err := exec.Command("git", "-C", parentCwd, "rev-parse", "--verify", "origin/HEAD").CombinedOutput(); err == nil && strings.TrimSpace(string(output)) != "" {
		base = "origin/HEAD"
	}
	cmd := exec.Command("git", "-C", parentCwd, "worktree", "add", "--detach", path, base)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("subagent: create worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	// 2.起始 commit 从 worktree 自身读出,避免主仓 HEAD 之后移动导致基线失真
	output, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		_ = exec.Command("git", "-C", parentCwd, "worktree", "remove", "--force", path).Run()
		return "", "", fmt.Errorf("subagent: resolve worktree head: %w", err)
	}
	return path, strings.TrimSpace(string(output)), nil
}

// metadataPathFor 返回任务元数据文件的固定位置
func metadataPathFor(_ string, sessionDir, id string) string {
	return filepath.Join(sessionDir, "subagents", "agent-"+id+".meta.json")
}

// cleanupSubagentWorktree 回收干净 worktree;有未提交改动或未合并提交时保留用户工作
func cleanupSubagentWorktree(path, parentCwd, baseCommit, metadataPath string) bool {
	status, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	if len(status) != 0 {
		return false
	}
	// 2.worktree 里新产生的 commit 还没进主仓,和未提交改动一样属于用户工作
	if !worktreeAtBaseCommit(path, baseCommit) {
		return false
	}
	if err := exec.Command("git", "-C", parentCwd, "worktree", "remove", "--force", path).Run(); err != nil {
		return false
	}
	_ = os.Remove(metadataPath)
	return true
}

// worktreeAtBaseCommit 判断 worktree HEAD 是否仍停在起始 commit 上。
// 基线未知或 git 失败时返回 false,调用方据此保留 worktree
func worktreeAtBaseCommit(path, baseCommit string) bool {
	if baseCommit == "" {
		return false
	}
	output, err := exec.Command("git", "-C", path, "rev-list", "--count", baseCommit+"..HEAD").Output()
	if err != nil {
		return false
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return false
	}
	return count == 0
}
