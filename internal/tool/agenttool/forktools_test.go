package agenttool

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/session"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

func TestForkChildKeepsAgentAndTaskStop(t *testing.T) {
	sm := session.New(t.TempDir(), t.TempDir())
	names := map[string]bool{}
	for _, candidate := range childTools(testParentTools(), true, sm, taskpkg.NewStore()) {
		names[candidate.Name()] = true
	}
	// fork 保留 agent 与 TaskStop,与父工具数组保持一致
	for _, want := range []string{"agent", "TaskStop"} {
		if !names[want] {
			t.Fatalf("fork child missing %q", want)
		}
	}
}
