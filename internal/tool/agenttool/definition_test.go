package agenttool

import "testing"

// agent 定义里的 thinking 必须是受支持的值,拼错应在加载期报错
func TestParseDefinitionRejectsUnknownThinking(t *testing.T) {
	if _, err := parseDefinition("---\nthinking: medum\n---\nbody", "custom"); err == nil {
		t.Fatal("invalid thinking level was accepted")
	}
	definition, err := parseDefinition("---\nthinking: high\n---\nbody", "custom")
	if err != nil {
		t.Fatalf("valid thinking level was rejected: %v", err)
	}
	if definition.Thinking != "high" {
		t.Fatalf("thinking = %q, want high", definition.Thinking)
	}
}
