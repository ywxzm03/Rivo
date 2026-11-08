package agenttool

import "testing"

func TestSubagentTypeAcceptsCustomNames(t *testing.T) {
	schema := New(nil).Parameters()
	properties := schema["properties"].(map[string]any)
	subagentType := properties["subagent_type"].(map[string]any)
	// 不能锁死 enum,否则 .rivo/agents 下的自定义 agent 无法被选中
	if _, ok := subagentType["enum"]; ok {
		t.Fatal("subagent_type must not restrict values to the built-in set")
	}
}
