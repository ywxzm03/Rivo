package adapter

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/provider"
)

func TestFileRestoreSummaryIncludesFailures(t *testing.T) {
	summary := fileRestoreSummary(&filehistory.RestoreResult{
		FilesChanged: []string{"changed"},
		FilesSkipped: []string{"skipped"},
		FilesFailed:  []string{"failed"},
	})
	if !strings.Contains(summary, "1 changed") || !strings.Contains(summary, "1 skipped") || !strings.Contains(summary, "1 failed") {
		t.Fatalf("summary = %q, want all restore counts", summary)
	}
}

func TestRuntimeWarningEventProjectsNotice(t *testing.T) {
	runtime := New(provider.Config{}, "/project", "main")
	if err := runtime.handleEvent(llm.WarningEvent{Message: "snapshot unavailable"}); err != nil {
		t.Fatal(err)
	}
	if len(runtime.messages) != 1 || runtime.messages[0].Role != "notice" || runtime.messages[0].Text != "Warning: snapshot unavailable" {
		t.Fatalf("messages = %+v, want warning notice", runtime.messages)
	}
}
