package compaction

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestCollectFileOperationsUsesPILists(t *testing.T) {
	var ops = newFileOperations()
	collectFileOperations([]llm.Message{
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{Name: "read", Arguments: map[string]any{"path": "z.go"}},
			llm.ToolCall{Name: "read", Arguments: map[string]any{"path": "a.go"}},
			llm.ToolCall{Name: "edit", Arguments: map[string]any{"path": "z.go"}},
			llm.ToolCall{Name: "write", Arguments: map[string]any{"path": "b.go"}},
			llm.ToolCall{Name: "bash", Arguments: map[string]any{"path": "ignored.go"}},
		}},
	}, &ops)

	got := computeFileLists(ops)
	if strings.Join(got.ReadFiles, ",") != "a.go" {
		t.Errorf("readFiles = %v, want [a.go]", got.ReadFiles)
	}
	if strings.Join(got.ModifiedFiles, ",") != "b.go,z.go" {
		t.Errorf("modifiedFiles = %v, want [b.go z.go]", got.ModifiedFiles)
	}
}

func TestSummaryWithFileListsCarriesPreviousDetails(t *testing.T) {
	summary, lists := summaryWithFileLists("SUMMARY", &FileLists{ReadFiles: []string{"old.go"}}, []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{Name: "write", Arguments: map[string]any{"path": "old.go"}},
			llm.ToolCall{Name: "read", Arguments: map[string]any{"path": "new.go"}},
		}},
	})
	if len(lists.ReadFiles) != 1 || lists.ReadFiles[0] != "new.go" {
		t.Errorf("readFiles = %v, want [new.go]", lists.ReadFiles)
	}
	if len(lists.ModifiedFiles) != 1 || lists.ModifiedFiles[0] != "old.go" {
		t.Errorf("modifiedFiles = %v, want [old.go]", lists.ModifiedFiles)
	}
	if !strings.Contains(summary, "<modified-files>\nold.go\n</modified-files>") {
		t.Errorf("summary = %q, want modified file section", summary)
	}
}
