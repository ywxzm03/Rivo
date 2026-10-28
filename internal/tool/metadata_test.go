package tool

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 每个工具都必须提供完整的元数据：模型靠这些字段决定是否调用它
func TestEveryToolExposesCompleteMetadata(t *testing.T) {
	tools := []llm.Tool{
		&Bash{}, &Edit{}, &Find{}, &Grep{}, &Ls{}, &PowerShell{}, &Read{}, &Write{},
	}
	for _, candidate := range tools {
		t.Run(candidate.Name(), func(t *testing.T) {
			if name := strings.TrimSpace(candidate.Name()); name == "" {
				t.Fatal("Name() = empty")
			}
			if strings.TrimSpace(candidate.Description()) == "" {
				t.Error("Description() = empty, want a human-readable summary")
			}
			params := candidate.Parameters()
			if params == nil {
				t.Fatal("Parameters() = nil, want a JSON schema")
			}
			if params["type"] != "object" {
				t.Errorf("Parameters()[type] = %v, want object", params["type"])
			}
			// 工具的说明分节可以为空，但方法必须可调用且不 panic
			_ = candidate.PromptSnippet()
			_ = candidate.PromptGuidelines()
			if mode := candidate.ExecutionMode(); mode != "" && mode != llm.ToolExecutionParallel && mode != llm.ToolExecutionSequential {
				t.Errorf("ExecutionMode() = %q, want a known mode", mode)
			}
		})
	}
}

// 同名工具不能重复注册，否则模型会看到两个同名函数
func TestToolNamesAreUnique(t *testing.T) {
	tools := []llm.Tool{
		&Bash{}, &Edit{}, &Find{}, &Grep{}, &Ls{}, &PowerShell{}, &Read{}, &Write{},
	}
	seen := make(map[string]bool, len(tools))
	for _, candidate := range tools {
		name := candidate.Name()
		if seen[name] {
			t.Errorf("duplicate tool name %q", name)
		}
		seen[name] = true
	}
}
