package responses

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// TestBuildParamsRendersSystemMessageByPosition 序列最前的 system 消息渲染完整文本，其余渲染补丁框定
func TestBuildParamsRendersSystemMessageByPosition(t *testing.T) {
	t.Run("leading system message renders full text", func(t *testing.T) {
		c := llm.Context{Messages: []llm.Message{
			llm.SystemMessage{Sections: map[string]string{"preamble": "base", "cwd": "/work"}},
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hi"}}},
		}}
		params, err := BuildParams(testModel(), c, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","input":[`+
				`{"role":"system","content":"base\n\n/work"},`+
				`{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	})

	t.Run("later system message renders update framing", func(t *testing.T) {
		c := llm.Context{Messages: []llm.Message{
			llm.SystemMessage{Sections: map[string]string{"preamble": "base"}},
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hi"}}},
			llm.SystemMessage{Sections: map[string]string{"cwd": "/new"}},
		}}
		params, err := BuildParams(testModel(), c, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","input":[`+
				`{"role":"system","content":"base"},`+
				`{"role":"user","content":[{"type":"input_text","text":"hi"}]},`+
				`{"role":"system","content":"Updated system prompt section \"cwd\":\n\n/new"}]}`)
	})

	t.Run("removal renders removal framing", func(t *testing.T) {
		c := llm.Context{Messages: []llm.Message{
			llm.SystemMessage{Sections: map[string]string{"preamble": "base"}},
			llm.SystemMessage{Sections: map[string]string{"skills": ""}},
		}}
		params, err := BuildParams(testModel(), c, llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		assertWire(t, params,
			`{"model":"gpt-5","input":[`+
				`{"role":"system","content":"base"},`+
				`{"role":"system","content":"Removed system prompt section \"skills\"."}]}`)
	})
}

// TestBuildParamsKeepsSystemPromptEntryFirst 入口字符串（压缩器路径）仍折成首条 system 消息
func TestBuildParamsKeepsSystemPromptEntryFirst(t *testing.T) {
	c := llm.Context{
		SystemPrompt: "summarizer",
		Messages:     []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "text"}}}},
	}
	params, err := BuildParams(testModel(), c, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertWire(t, params,
		`{"model":"gpt-5","input":[`+
			`{"role":"system","content":"summarizer"},`+
			`{"role":"user","content":[{"type":"input_text","text":"text"}]}]}`)
}
