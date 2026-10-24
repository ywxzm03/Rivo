package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSubagentResumeResolverReadsMetadata(t *testing.T) {
	dir := t.TempDir()
	subagents := filepath.Join(dir, "subagents")
	if err := os.MkdirAll(subagents, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(subagents, "agent-agent-1.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(subagents, "agent-agent-1.meta.json")
	if err := os.WriteFile(metadata, []byte(`{"agentType":"explore","description":"scan tree","worktreePath":"/tmp/wt"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := subagentResumeResolver(dir)
	source, ok := resolver("agent-1")
	if !ok {
		t.Fatal("existing transcript was not resolved")
	}
	if source.AgentType != "explore" || source.Description != "scan tree" || source.WorktreePath != "/tmp/wt" {
		t.Fatalf("source = %#v", source)
	}
	if source.OutputFile != transcript {
		t.Fatalf("transcript path = %q, want %q", source.OutputFile, transcript)
	}
}

func TestSubagentResumeResolverFallsBackWithoutMetadata(t *testing.T) {
	dir := t.TempDir()
	subagents := filepath.Join(dir, "subagents")
	if err := os.MkdirAll(subagents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagents, "agent-agent-2.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, ok := subagentResumeResolver(dir)("agent-2")
	if !ok || source.AgentType != "work" {
		t.Fatalf("source = %#v ok = %v", source, ok)
	}
}

func TestSubagentResumeResolverRejectsMissingTranscriptAndTraversal(t *testing.T) {
	dir := t.TempDir()
	resolver := subagentResumeResolver(dir)
	if _, ok := resolver("agent-missing"); ok {
		t.Fatal("missing transcript was resolved")
	}
	if _, ok := resolver("../escape"); ok {
		t.Fatal("path traversal was resolved")
	}
}
