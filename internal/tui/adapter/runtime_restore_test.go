package adapter

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/filehistory"
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
