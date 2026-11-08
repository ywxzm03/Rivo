package tui

import (
	"strings"
	"testing"
)

func TestCodemodeLongHeaderKeepsResultVisible(t *testing.T) {
	call := codemodeRow()
	call.Arguments = `{"code":"text(\"` + strings.Repeat("x", 10000) + `\");"}`
	lines := renderToolRow(call, 80, false, call.StartedAt)
	if len(lines) > 12 || !strings.Contains(strings.Join(lines, "\n"), "Done in 1.234s") {
		t.Fatalf("source header displaced the result: %d rendered rows", len(lines))
	}
}
