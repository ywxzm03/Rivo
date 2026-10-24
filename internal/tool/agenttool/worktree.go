package agenttool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// createSubagentWorktree 为任务创建 detached worktree,优先以 origin/HEAD 为基线
func createSubagentWorktree(parentCwd, sessionDir, id string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("subagent: git is unavailable for worktree: %w", err)
	}
	path := filepath.Join(sessionDir, "subagents", "worktree-"+id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// 1.有远端默认分支时以它为准,否则退回本地 HEAD
	base := "HEAD"
	if output, err := exec.Command("git", "-C", parentCwd, "rev-parse", "--verify", "origin/HEAD").CombinedOutput(); err == nil && strings.TrimSpace(string(output)) != "" {
		base = "origin/HEAD"
	}
	cmd := exec.Command("git", "-C", parentCwd, "worktree", "add", "--detach", path, base)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("subagent: create worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return path, nil
}

// metadataPathFor 返回任务元数据文件的固定位置
func metadataPathFor(_ string, sessionDir, id string) string {
	return filepath.Join(sessionDir, "subagents", "agent-"+id+".meta.json")
}

// cleanupSubagentWorktree 回收干净 worktree;有未提交改动时保留用户工作
func cleanupSubagentWorktree(path, parentCwd, metadataPath string) bool {
	status, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	if len(status) != 0 {
		return false
	}
	if err := exec.Command("git", "-C", parentCwd, "worktree", "remove", "--force", path).Run(); err != nil {
		return false
	}
	_ = os.Remove(metadataPath)
	return true
}
