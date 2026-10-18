package responses

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// assertWire 断言 v 序列化后的 JSON 与 want 深度相等，键序无关
func assertWire(t *testing.T, v any, want string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(b, &gotAny); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantAny); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("wire JSON mismatch\ngot  %s\nwant %s", b, want)
	}
}

func testModel() llm.Model {
	return llm.Model{ID: "gpt-5", Name: "GPT-5", Provider: "openai", API: llm.APIOpenAIResponses}
}

// fakeTool 是 llm.Tool 的最小实现，用来断言工具定义的线格式
type fakeTool struct {
	name        string
	description string
	parameters  map[string]any
}

func (t fakeTool) Name() string               { return t.name }
func (t fakeTool) Description() string        { return t.description }
func (t fakeTool) Parameters() map[string]any { return t.parameters }
func (t fakeTool) PromptSnippet() string      { return "" }
func (t fakeTool) PromptGuidelines() []string { return nil }
func (t fakeTool) ExecutionMode() llm.ToolExecutionMode {
	return llm.ToolExecutionParallel
}

func (t fakeTool) Execute(context.Context, map[string]any) (llm.ToolResult, error) {
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "pong"}}}, nil
}

var _ llm.Tool = fakeTool{}

func TestBuildParams(t *testing.T) {
	pingTool := fakeTool{
		name:        "ping",
		description: "Return pong",
		parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}

	t.Run("system prompt generates the first system message", func(t *testing.T) {
		params, err := BuildParams(testModel(), llm.Context{SystemPrompt: "be brief"}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,

			`{"model":"gpt-5","input":[{"role":"system","content":"be brief"}]}`)
	})

	t.Run("user text and image merge into one message", func(t *testing.T) {
		c := llm.Context{Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{
			llm.TextContent{Text: "look at this image"},
			llm.ImageContent{Data: "aGk=", MIMEType: "image/png"},
		}}}}
		params, err := BuildParams(testModel(), c, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","input":[{"role":"user","content":[`+
				`{"type":"input_text","text":"look at this image"},`+
				`{"type":"input_image","image_url":"data:image/png;base64,aGk=","detail":"auto"}]}]}`)
	})

	t.Run("assistant message splits into message and function_call", func(t *testing.T) {
		c := llm.Context{Messages: []llm.Message{llm.AssistantMessage{Content: []llm.Content{
			llm.TextContent{Text: "let me try"},
			llm.ToolCall{ID: "call_1", Name: "ping", Arguments: map[string]any{}},
		}}}}
		params, err := BuildParams(testModel(), c, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","input":[`+
				`{"type":"message","role":"assistant","status":"completed","id":"msg_rivo_0",`+
				`"content":[{"type":"output_text","text":"let me try","annotations":[]}]},`+
				`{"type":"function_call","arguments":"{}","call_id":"call_1","name":"ping"}]}`)
	})

	t.Run("tool result maps to function_call_output", func(t *testing.T) {
		c := llm.Context{Messages: []llm.Message{llm.ToolResultMessage{
			ToolCallID: "call_1",
			ToolName:   "ping",
			Content:    []llm.Content{llm.TextContent{Text: "pong"}},
		}}}
		params, err := BuildParams(testModel(), c, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"call_1","output":"pong"}]}`)
	})

	t.Run("tool result with image switches to structured output", func(t *testing.T) {
		c := llm.Context{Messages: []llm.Message{llm.ToolResultMessage{
			ToolCallID: "call_1",
			ToolName:   "read",
			Content: []llm.Content{
				llm.TextContent{Text: "Read image file [image/png]"},
				llm.ImageContent{Data: "aGk=", MIMEType: "image/png"},
			},
		}}}
		params, err := BuildParams(testModel(), c, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"call_1","output":[`+
				`{"type":"input_text","text":"Read image file [image/png]"},`+
				`{"type":"input_image","image_url":"data:image/png;base64,aGk=","detail":"auto"}]}]}`)
	})

	t.Run("tool definition converts to function tool", func(t *testing.T) {
		params, err := BuildParams(testModel(), llm.Context{Tools: []llm.Tool{pingTool}}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","tools":[{"type":"function","name":"ping","description":"Return pong","parameters":{"properties":{},"type":"object"}}]}`)
	})

	t.Run("assistant messages take ids by index across turns", func(t *testing.T) {
		c := llm.Context{Messages: []llm.Message{
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "pi"}}},
			llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "3.14159"}}},
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}},
		}}
		params, err := BuildParams(testModel(), c, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","input":[`+
				`{"role":"user","content":[{"type":"input_text","text":"pi"}]},`+
				`{"type":"message","role":"assistant","status":"completed","id":"msg_rivo_1",`+
				`"content":[{"type":"output_text","text":"3.14159","annotations":[]}]},`+
				`{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	})

	t.Run("reasoning request enables summary and encrypted content", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		model.ReasoningEffort = llm.ReasoningEffortHigh
		params, err := BuildParams(model, llm.Context{}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("DeepSeek reasoning model uses Responses request", func(t *testing.T) {
		model := llm.Model{
			ID:              "deepseek-flash",
			Provider:        "deepseek",
			API:             llm.APIOpenAIResponses,
			Reasoning:       true,
			ReasoningEffort: llm.ReasoningEffortMedium,
		}
		params, err := BuildParams(model, llm.Context{}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params, `{"model":"deepseek-flash","reasoning":{"effort":"medium","summary":"auto"},"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("model default effort passes through without validation", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		model.ReasoningEffort = llm.ReasoningEffort("ultra")
		params, err := BuildParams(model, llm.Context{}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		// 对齐 pi：模型默认 effort 原样透传，由后端决定是否接受
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"ultra","summary":"auto"},"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("thinking level maps to configured effort", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		model.ThinkingLevelMap = map[llm.ThinkingLevel]*llm.ReasoningEffort{
			llm.ThinkingLevelMedium: effortPtr("custom-effort"),
		}
		params, err := BuildParams(model, llm.Context{}, llm.Options{ThinkingLevel: llm.ThinkingLevelMedium})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"custom-effort","summary":"auto"},"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("unmapped thinking level passes through", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		model.ThinkingLevelMap = map[llm.ThinkingLevel]*llm.ReasoningEffort{
			llm.ThinkingLevelHigh: effortPtr("high-plus"),
		}
		params, err := BuildParams(model, llm.Context{}, llm.Options{ThinkingLevel: llm.ThinkingLevelLow})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"low","summary":"auto"},"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("off without map sends effort none", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		params, err := BuildParams(model, llm.Context{}, llm.Options{ThinkingLevel: llm.ThinkingLevelOff})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"none","summary":"auto"},"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("off mapped to null clamps to minimal", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		model.ThinkingLevelMap = map[llm.ThinkingLevel]*llm.ReasoningEffort{
			llm.ThinkingLevelOff: nil,
		}
		params, err := BuildParams(model, llm.Context{}, llm.Options{ThinkingLevel: llm.ThinkingLevelOff})
		if err != nil {
			t.Fatal(err)
		}
		// pi 语义：off 被禁用时请求 off 收敛到最小可用档 minimal
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"minimal","summary":"auto"},"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("xhigh without explicit mapping clamps down to high", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		model.ThinkingLevelMap = map[llm.ThinkingLevel]*llm.ReasoningEffort{
			llm.ThinkingLevelHigh: effortPtr("high-plus"),
		}
		params, err := BuildParams(model, llm.Context{}, llm.Options{ThinkingLevel: llm.ThinkingLevelMax})
		if err != nil {
			t.Fatal(err)
		}
		// xhigh/max 须显式映射才可用：max 不可用，向上无可用档，向下收敛到 high
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"high-plus","summary":"auto"},"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("thinking level on non reasoning model sends no reasoning", func(t *testing.T) {
		model := testModel()
		params, err := BuildParams(model, llm.Context{}, llm.Options{ThinkingLevel: llm.ThinkingLevelHigh})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params, `{"model":"gpt-5"}`)
	})

	t.Run("sampling params merge into request", func(t *testing.T) {
		model := testModel()
		model.SamplingParams = map[string]any{"temperature": 0.5, "top_k": 20}
		params, err := BuildParams(model, llm.Context{}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params, `{"model":"gpt-5","temperature":0.5,"top_k":20}`)
	})

	t.Run("sampling params cannot override reserved request fields", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		model.SamplingParams = map[string]any{
			"model": "evil", "reasoning": "evil", "temperature": 0.5,
		}
		params, err := BuildParams(model, llm.Context{}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		// 保留键被跳过，核心字段保持 BuildParams 构建的值；其余自由键正常并入
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"medium","summary":"auto"},"temperature":0.5,"include":["reasoning.encrypted_content"]}`)
	})

	t.Run("assistant reasoning item replays verbatim", func(t *testing.T) {
		model := testModel()
		model.Reasoning = true
		signature := `{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"summary"}],"content":[{"type":"reasoning_text","text":"cot"}],"encrypted_content":"enc","status":"completed"}`
		params, err := BuildParams(model, llm.Context{Messages: []llm.Message{
			llm.AssistantMessage{Content: []llm.Content{
				llm.ThinkingContent{Thinking: "summary", ThinkingSignature: signature},
				llm.TextContent{Text: "answer"},
			}},
		}}, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params, `{"model":"gpt-5","reasoning":{"effort":"medium","summary":"auto"},"include":["reasoning.encrypted_content"],"input":[`+
			`{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"summary"}],"encrypted_content":"enc","content":[{"type":"reasoning_text","text":"cot"}],"status":"completed"},`+
			`{"type":"message","role":"assistant","status":"completed","id":"msg_rivo_0","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`)
	})
}

func responseFromJSON(t *testing.T, s string) responses.Response {
	t.Helper()
	var resp responses.Response
	if err := json.Unmarshal([]byte(s), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

func TestBuildAssistantMessage(t *testing.T) {
	t.Run("reasoning summary takes precedence and keeps the original item", func(t *testing.T) {
		resp := responseFromJSON(t, `{
			"id":"resp_reasoning","created_at":0,"status":"completed",
			"output":[{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"summary"}],
			 "content":[{"type":"reasoning_text","text":"cot"}],"encrypted_content":"enc","status":"completed"}],
			"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}
		}`)
		msg, _, err := BuildAssistantMessage(resp)
		if err != nil {
			t.Fatal(err)
		}
		thinking, ok := msg.Content[0].(llm.ThinkingContent)
		if !ok || thinking.Thinking != "summary" || thinking.ThinkingSignature == "" {
			t.Fatalf("thinking = %#v", msg.Content[0])
		}
		if msg.ResponseID != "resp_reasoning" {
			t.Errorf("ResponseID = %q", msg.ResponseID)
		}
	})

	t.Run("text and tool calls with stopReason toolUse", func(t *testing.T) {
		resp := responseFromJSON(t, `{
			"id":"resp_1","created_at":0,"status":"completed",
			"output":[
				{"type":"message","id":"m1","role":"assistant","status":"completed",
				 "content":[{"type":"output_text","text":"let me try"}]},
				{"type":"function_call","id":"fc1","call_id":"call_1","name":"ping","arguments":"{\"k\":1}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
		}`)
		msg, calls, err := BuildAssistantMessage(resp)
		if err != nil {
			t.Fatal(err)
		}
		if msg.StopReason != llm.StopReasonToolUse {
			t.Errorf("StopReason = %q, want toolUse", msg.StopReason)
		}
		if msg.Usage != (llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}) {
			t.Errorf("Usage = %+v", msg.Usage)
		}
		if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "ping" {
			t.Fatalf("calls = %+v", calls)
		}
		if calls[0].Arguments["k"] != float64(1) {
			t.Errorf("Arguments = %+v", calls[0].Arguments)
		}
		if len(msg.Content) != 2 {
			t.Errorf("Content = %+v", msg.Content)
		}
	})

	t.Run("incomplete truncation marked as length", func(t *testing.T) {
		resp := responseFromJSON(t, `{
			"id":"resp_2","created_at":0,"status":"incomplete",
			"incomplete_details":{"reason":"max_output_tokens"},
			"output":[{"type":"message","id":"m2","role":"assistant","status":"incomplete",
				"content":[{"type":"output_text","text":"partial"}]}],
			"usage":{"input_tokens":7,"output_tokens":100,"total_tokens":107}
		}`)
		msg, _, err := BuildAssistantMessage(resp)
		if err != nil {
			t.Fatal(err)
		}
		if msg.StopReason != llm.StopReasonLength {
			t.Errorf("StopReason = %q, want length", msg.StopReason)
		}
	})

	t.Run("empty arguments parse to an empty map", func(t *testing.T) {
		resp := responseFromJSON(t, `{
			"id":"resp_3","created_at":0,"status":"completed",
			"output":[{"type":"function_call","id":"fc3","call_id":"call_3","name":"ping","arguments":""}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)
		_, calls, err := BuildAssistantMessage(resp)
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || len(calls[0].Arguments) != 0 {
			t.Errorf("calls = %+v", calls)
		}
	})

	t.Run("invalid arguments JSON returns an error", func(t *testing.T) {
		resp := responseFromJSON(t, `{
			"id":"resp_4","created_at":0,"status":"completed",
			"output":[{"type":"function_call","id":"fc4","call_id":"call_4","name":"ping","arguments":"{bad"}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)
		if _, _, err := BuildAssistantMessage(resp); err == nil {
			t.Error("expected an error for invalid JSON")
		}
	})

	t.Run("custom tool returns an unsupported error", func(t *testing.T) {
		resp := responseFromJSON(t, `{
			"id":"resp_custom","created_at":0,"status":"completed",
			"output":[{"type":"custom_tool_call","id":"ct1","call_id":"call_custom","name":"apply_patch","input":"patch"}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)
		if _, _, err := BuildAssistantMessage(resp); err == nil {
			t.Error("expected an unsupported error for custom tool")
		}
	})
}

func TestAssistantMessageReplayRoundTrip(t *testing.T) {
	resp := responseFromJSON(t, `{
		"id":"resp_1","created_at":0,"status":"completed",
		"output":[{"type":"message","id":"msg_abc","role":"assistant","status":"completed",
			"content":[{"type":"output_text","text":"pi is approximately 3.14159"}]}],
		"usage":{"input_tokens":3,"output_tokens":9,"total_tokens":12}
	}`)

	msg, _, err := BuildAssistantMessage(resp)
	if err != nil {
		t.Fatal(err)
	}

	params, err := BuildParams(testModel(), llm.Context{Messages: []llm.Message{*msg}}, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}

	assertWire(t, params,
		`{"model":"gpt-5","input":[`+
			`{"type":"message","role":"assistant","status":"completed","id":"msg_rivo_0",`+
			`"content":[{"type":"output_text","text":"pi is approximately 3.14159","annotations":[]}]}]}`)
}

// effortPtr 返回 effort 字符串的指针，便于构造 thinkingLevelMap
func effortPtr(e llm.ReasoningEffort) *llm.ReasoningEffort {
	return &e
}
