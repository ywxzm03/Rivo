package llm

import "testing"

// CloneContent 必须递归隔离 tool 参数里的嵌套容器
func TestCloneContentDeepCopiesArguments(t *testing.T) {
	original := []Content{ToolCall{
		ID:   "c1",
		Name: "write",
		Arguments: map[string]any{
			"path": "a.txt",
			"meta": map[string]any{"tags": []any{"x", "y"}},
		},
	}}
	cloned := CloneContent(original)
	call := cloned[0].(ToolCall)
	meta := call.Arguments["meta"].(map[string]any)
	meta["tags"].([]any)[0] = "mutated"

	source := original[0].(ToolCall)
	if source.Arguments["meta"].(map[string]any)["tags"].([]any)[0] != "x" {
		t.Fatal("nested argument was shared between the clone and the source")
	}
}

// nil 参数保持 nil,不能被替换成空 map
func TestCloneContentPreservesNilArguments(t *testing.T) {
	cloned := CloneContent([]Content{ToolCall{ID: "c1", Name: "noop"}})
	if cloned[0].(ToolCall).Arguments != nil {
		t.Fatal("nil arguments became non-nil")
	}
}
