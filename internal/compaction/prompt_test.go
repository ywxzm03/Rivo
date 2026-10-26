package compaction

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/ctxview"
)

func TestBuildSummarizePrompt(t *testing.T) {
	got := BuildSummarizePrompt("[User]: hi", "")
	for _, want := range []string{
		"<conversation>\n[User]: hi\n</conversation>",
		"## Goal",
		"## Constraints & Preferences",
		"## Progress",
		"### Done",
		"### In Progress",
		"### Blocked",
		"## Key Decisions",
		"## Next Steps",
		"## Critical Context",
		"Preserve exact file paths",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt = %q, want contains %q", got, want)
		}
	}
	if strings.Contains(got, "Additional focus") {
		t.Error("empty instructions should not add focus")
	}
	withFocus := BuildSummarizePrompt("[User]: hi", "the test suite")
	if !strings.Contains(withFocus, "\n\nAdditional focus: the test suite") {
		t.Errorf("prompt = %q, want additional focus", withFocus)
	}
}

func TestBuildUpdatePrompt(t *testing.T) {
	got := BuildUpdatePrompt("old summary", "[User]: hi", "")
	if !strings.Contains(got, "<previous-summary>\nold summary\n</previous-summary>") {
		t.Errorf("prompt = %q, want previous-summary block", got)
	}
	if !strings.Contains(got, "Merge the new information into the existing summary") {
		t.Errorf("prompt = %q, want merge instruction", got)
	}
}

func TestBuildTurnPrefixPrompt(t *testing.T) {
	got := BuildTurnPrefixPrompt("[User]: hi")
	if !strings.Contains(got, "beginning of the current conversation turn") {
		t.Errorf("prompt = %q, want turn prefix instruction", got)
	}
}

func TestMergeSummaries(t *testing.T) {
	got := MergeSummaries("history", "prefix")
	if got != "history\n\n---\n\n**Turn Context (split turn):**\n\nprefix" {
		t.Errorf("MergeSummaries = %q", got)
	}
}

func TestWrappedSummary(t *testing.T) {
	got := ctxview.WrappedSummary("content")
	want := "The conversation history before this point was compacted into the following summary:\n\n<summary>\ncontent\n</summary>"
	if got != want {
		t.Errorf("ctxview.WrappedSummary = %q, want %q", got, want)
	}
}
