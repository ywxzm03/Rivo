package tui

import (
	"strings"
	"testing"
	"time"
)

func TestCodemodeTitleUsesFirstNonemptyCodeLine(t *testing.T) {
	for _, tc := range []struct {
		name, code, want string
	}{
		{"single", "\n  text('你好');\n\n", `codemode(code: "text('你好');")`},
		{"multiple", "\nconst result = 1;\n\ntext(result);", `codemode(code: "const result = 1; …")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := rowCall("parent", "codemode", map[string]any{"code": tc.code}, ToolSuccess)
			for _, expanded := range []bool{false, true} {
				lines := rowLines(call, expanded)
				if !strings.Contains(lines[0], tc.want) {
					t.Fatalf("title = %q, want %q", lines[0], tc.want)
				}
				if strings.Contains(lines[0], `\n`) || strings.Contains(lines[0], "Script") {
					t.Fatalf("title contains source metadata: %q", lines[0])
				}
			}
		})
	}
}

func TestCodemodePreviewFourRowsVisibleFiveRowsFold(t *testing.T) {
	call := rowCall("parent", "codemode", map[string]any{"code": "text('output');"}, ToolSuccess)
	call.Result = "row-one\nrow-two\nrow-three\nrow-four"
	four := strings.Join(rowLines(call, false), "\n")
	if !strings.Contains(four, "row-four") || strings.Contains(four, "ctrl+o") {
		t.Fatalf("four rendered rows must remain visible:\n%s", four)
	}
	call.Result += "\nrow-five"
	five := strings.Join(rowLines(call, false), "\n")
	if !strings.Contains(five, "row-three") || strings.Contains(five, "row-four") || strings.Contains(five, "row-five") || !strings.Contains(five, "+2 lines") {
		t.Fatalf("five rendered rows must preview three:\n%s", five)
	}
}

func TestCodemodePreviewBudgetCountsWrappedRows(t *testing.T) {
	call := rowCall("parent", "codemode", map[string]any{"code": "text('output');"}, ToolSuccess)
	for _, tc := range []struct {
		rows     int
		wantFold bool
	}{{4, false}, {5, true}} {
		call.Result = strings.Repeat("界", 17*tc.rows)
		got := strings.Join(plainLines(strings.Join(renderToolRow(call, 44, false, time.Unix(0, 0)), "\n")), "\n")
		if strings.Contains(got, "ctrl+o") != tc.wantFold {
			t.Fatalf("%d wrapped rows fold=%v:\n%s", tc.rows, tc.wantFold, got)
		}
		if tc.wantFold && !strings.Contains(got, "+2 lines") {
			t.Fatalf("hidden count used logical instead of rendered rows:\n%s", got)
		}
	}
}
