package registry

import (
	"encoding/json"
	"reflect"
	"runtime"
	"testing"

	"github.com/ywxzm03/Rivo/internal/tool/codemode"
)

type codemodeHistory struct{}

func (codemodeHistory) BranchToolDetails(string) ([]json.RawMessage, error) { return nil, nil }

func TestBuildCodemodeKeepsDirectTools(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		tools := Build(Deps{WorkingDir: t.TempDir(), CodemodeEnabled: enabled, CodemodeHistory: codemodeHistory{}})
		var direct []string
		count := 0
		for _, tool := range tools {
			if tool.Name() == "codemode" {
				count++
				if _, ok := tool.(*codemode.Tool); !ok {
					t.Fatalf("codemode implementation = %T", tool)
				}
				continue
			}
			direct = append(direct, tool.Name())
		}
		want := []string{"read", "ls", "grep", "find", "bash", "write", "edit", "agent", "SendMessage", "TaskStop"}
		if runtime.GOOS == "windows" {
			want = append(want, "powershell")
		}
		if !reflect.DeepEqual(direct, want) {
			t.Fatalf("enabled=%v: direct tools = %v, want %v", enabled, direct, want)
		}
		wantCount := 0
		if enabled {
			wantCount = 1
		}
		if count != wantCount {
			t.Fatalf("enabled=%v: codemode count = %d, want %d", enabled, count, wantCount)
		}
	}
}
