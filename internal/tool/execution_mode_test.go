package tool

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestBuiltInToolsDeclareParallelExecution(t *testing.T) {
	tools := []llm.Tool{
		&Bash{},
		&Edit{},
		&Find{},
		&Grep{},
		&Ls{},
		&PowerShell{},
		&Read{},
		&Write{},
	}

	for _, tool := range tools {
		if got := tool.ExecutionMode(); got != llm.ToolExecutionParallel {
			t.Errorf("%s.ExecutionMode() = %q, want %q", tool.Name(), got, llm.ToolExecutionParallel)
		}
	}
}
