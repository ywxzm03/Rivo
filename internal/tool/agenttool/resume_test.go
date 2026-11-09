package agenttool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeResolverCarriesWorktreeOwnership(t *testing.T) {
	sessionDir := t.TempDir()
	dir := filepath.Join(sessionDir, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 1.子 agent 创建的 worktree 必须把归属信息带回 ResumeSource
	if err := os.WriteFile(filepath.Join(dir, "owned.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeMetadata(filepath.Join(dir, "owned.meta.json"), metadata{
		AgentType: "work", Description: "old", WorktreePath: "/wt", WorktreeOwned: true,
	}); err != nil {
		t.Fatal(err)
	}
	source, ok := ResumeResolver(sessionDir)("owned")
	if !ok {
		t.Fatal("metadata-backed task was not resolvable")
	}
	if source.WorktreePath != "/wt" || !source.WorktreeOwned {
		t.Fatalf("source = %#v, want owned worktree", source)
	}
	// 2.元数据缺失时退回 work,且不声称拥有任何 worktree
	if err := os.WriteFile(filepath.Join(dir, "bare.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, ok = ResumeResolver(sessionDir)("bare")
	if !ok {
		t.Fatal("transcript-only task was not resolvable")
	}
	if source.AgentType != "work" || source.WorktreePath != "" || source.WorktreeOwned {
		t.Fatalf("source = %#v, want bare work task", source)
	}
}
