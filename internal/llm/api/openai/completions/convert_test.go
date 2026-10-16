package completions

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type testTool struct{}

func (testTool) Name() string                         { return "read" }
func (testTool) Description() string                  { return "Read a file" }
func (testTool) Parameters() map[string]any           { return map[string]any{"type": "object"} }
func (testTool) PromptSnippet() string                { return "" }
func (testTool) PromptGuidelines() []string           { return nil }
func (testTool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }
func (testTool) Execute(context.Context, map[string]any) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func wireJSON(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuildParamsOpenAICacheAndMessages(t *testing.T) {
	model := llm.Model{ID: "gpt-5.6-sol", Provider: "openai", API: llm.APIOpenAICompletions}
	params, err := BuildParams(model, llm.Context{
		SystemPrompt: "Follow instructions",
		Messages:     []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}}},
	}, llm.Options{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "system", "content": "Follow instructions"},
			map[string]any{"role": "user", "content": "hello"},
		},
		"prompt_cache_key":     "session-1",
		"prompt_cache_options": map[string]any{"ttl": "30m"},
		"store":                false,
		"stream_options":       map[string]any{"include_usage": true},
	}
	got := wireJSON(t, params)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire = %#v, want %#v", got, want)
	}
}

func TestBuildParamsSendsTTLWithoutSessionID(t *testing.T) {
	model := llm.Model{ID: "gpt-6-luna", Provider: "openai", API: llm.APIOpenAICompletions}
	params, err := BuildParams(model, llm.Context{}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := wireJSON(t, params).(map[string]any)
	if got["prompt_cache_options"] == nil || got["prompt_cache_key"] != nil {
		t.Fatalf("cache fields = %#v", got)
	}
}

func TestBuildParamsDeepSeekThinking(t *testing.T) {
	model := llm.Model{ID: "deepseek-v4-pro", Provider: "deepseek", API: llm.APIOpenAICompletions, Reasoning: true}
	params, err := BuildParams(model, llm.Context{}, llm.Options{ThinkingLevel: llm.ThinkingLevelHigh})
	if err != nil {
		t.Fatal(err)
	}
	got := wireJSON(t, params).(map[string]any)
	if !reflect.DeepEqual(got["thinking"], map[string]any{"type": "enabled"}) {
		t.Fatalf("thinking = %#v", got["thinking"])
	}
	if got["prompt_cache_options"] != nil {
		t.Fatalf("DeepSeek cache fields = %#v", got["prompt_cache_options"])
	}
}

func TestBuildParamsIncludesTools(t *testing.T) {
	model := llm.Model{ID: "gpt-5.6-sol", Provider: "openai", API: llm.APIOpenAICompletions}
	params, err := BuildParams(model, llm.Context{Tools: []llm.Tool{testTool{}}}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := wireJSON(t, params).(map[string]any)
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", got["tools"])
	}
}
