package completions

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 指令消息的角色取决于厂商与推理能力
func TestInstructionRole(t *testing.T) {
	tests := []struct {
		name  string
		model llm.Model
		want  string
	}{
		{"openai 推理模型用 developer", llm.Model{Provider: "openai", Reasoning: true}, "developer"},
		{"openai 非推理模型用 system", llm.Model{Provider: "openai"}, "system"},
		{"其他厂商一律 system", llm.Model{Provider: "deepseek", Reasoning: true}, "system"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := instructionRole(tt.model); got != tt.want {
				t.Errorf("instructionRole() = %q, want %q", got, tt.want)
			}
		})
	}
}

// 单条纯文本走字符串快路径，多段与图片走结构化 parts
func TestUserContent(t *testing.T) {
	single, err := userContent([]llm.Content{llm.TextContent{Text: "hi"}})
	if err != nil {
		t.Fatalf("userContent(single text) error = %v", err)
	}
	if text, ok := single.(string); !ok || text != "hi" {
		t.Fatalf("userContent(single text) = %#v, want the plain string", single)
	}

	parts, err := userContent([]llm.Content{
		llm.TextContent{Text: "look"},
		llm.ImageContent{Data: "aGk=", MIMEType: "image/png"},
	})
	if err != nil {
		t.Fatalf("userContent(text+image) error = %v", err)
	}
	list, ok := parts.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("userContent(text+image) = %#v, want two parts", parts)
	}
	image, ok := list[1].(map[string]any)
	if !ok || image["type"] != "image_url" {
		t.Fatalf("image part = %#v, want an image_url part", list[1])
	}
	url, _ := image["image_url"].(map[string]any)
	if got, _ := url["url"].(string); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("image url = %q, want a base64 data URL", got)
	}

	if _, err := userContent([]llm.Content{llm.ThinkingContent{Thinking: "x"}}); err == nil {
		t.Error("userContent(unsupported) error = nil, want error")
	}
}

// assistant 消息必须带上文本、工具调用与思考内容
func TestAssistantMessage(t *testing.T) {
	model := llm.Model{Provider: "deepseek"}
	message := llm.AssistantMessage{Content: []llm.Content{
		llm.TextContent{Text: "answer"},
		llm.ThinkingContent{Thinking: "step one", ThinkingSignature: "reasoning_content"},
		llm.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"path": "a.go"}},
	}}
	got := assistantMessage(model, message)
	if got["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", got["role"])
	}
	if got["content"] != "answer" {
		t.Errorf("content = %v, want answer", got["content"])
	}
	if _, ok := got["tool_calls"]; !ok {
		t.Error("tool_calls missing")
	}
	if got["reasoning_content"] != "step one" {
		t.Errorf("reasoning_content = %v, want the thinking text", got["reasoning_content"])
	}

	// 非 deepseek/openai 厂商不发送思考内容
	other := assistantMessage(llm.Model{Provider: "magpie"}, message)
	if _, ok := other["reasoning_content"]; ok {
		t.Error("reasoning_content sent for an unsupported provider")
	}

	// 只有工具调用、没有文本时不应带 content 字段
	toolOnly := assistantMessage(model, llm.AssistantMessage{Content: []llm.Content{
		llm.ToolCall{ID: "call-2", Name: "read"},
	}})
	if _, ok := toolOnly["content"]; ok {
		t.Error("content present for a tool-only message")
	}
}

// 工具结果必须有文本；空结果回退到占位串
func TestToolResultMessage(t *testing.T) {
	got := toolResultMessage(llm.ToolResultMessage{
		ToolCallID: "call-1",
		Content:    []llm.Content{llm.TextContent{Text: "line one"}, llm.TextContent{Text: "line two"}},
	})
	if got["role"] != "tool" || got["tool_call_id"] != "call-1" {
		t.Fatalf("toolResultMessage() = %#v, want tool role with the call id", got)
	}
	if got["content"] != "line one\nline two" {
		t.Errorf("content = %q, want the joined lines", got["content"])
	}

	empty := toolResultMessage(llm.ToolResultMessage{ToolCallID: "call-2"})
	if empty["content"] != "(no tool output)" {
		t.Errorf("empty content = %q, want the placeholder", empty["content"])
	}
}

// 思考等级为 off 时不启用思考；空等级回退到模型默认，再回退到 medium
func TestThinkingEnabledAndReasoningEffort(t *testing.T) {
	if thinkingEnabled(llm.Options{ThinkingLevel: llm.ThinkingLevelOff}) {
		t.Error("thinkingEnabled(off) = true, want false")
	}
	if !thinkingEnabled(llm.Options{ThinkingLevel: llm.ThinkingLevelHigh}) {
		t.Error("thinkingEnabled(high) = false, want true")
	}

	model := llm.Model{ReasoningEffort: llm.ReasoningEffortHigh}
	if got := resolveReasoningEffort(model, llm.Options{}); got != llm.ReasoningEffortHigh {
		t.Errorf("resolveReasoningEffort(empty level) = %q, want the model default", got)
	}
	if got := resolveReasoningEffort(llm.Model{}, llm.Options{}); got != llm.ReasoningEffortMedium {
		t.Errorf("resolveReasoningEffort(no default) = %q, want medium", got)
	}
	if got := resolveReasoningEffort(model, llm.Options{ThinkingLevel: llm.ThinkingLevelLow}); got == "" {
		t.Error("resolveReasoningEffort(explicit level) = empty, want a concrete effort")
	}
}
