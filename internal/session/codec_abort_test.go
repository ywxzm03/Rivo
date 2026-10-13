package session

import (
	"encoding/json"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// aborted assistant 连同 ErrorMessage 往返序列化
func TestCodecAbortedAssistantRoundTrip(t *testing.T) {
	raw, err := encodeMessage(llm.AssistantMessage{
		Content:      []llm.Content{llm.TextContent{Text: "partial"}},
		StopReason:   llm.StopReasonAborted,
		ErrorMessage: "Operation aborted",
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	message := decoded.(llm.AssistantMessage)
	if message.StopReason != llm.StopReasonAborted || message.ErrorMessage != "Operation aborted" {
		t.Fatalf("round trip = %+v", message)
	}
	if text, ok := message.Content[0].(llm.TextContent); !ok || text.Text != "partial" {
		t.Fatalf("content = %+v", message.Content)
	}
}

// 旧会话文件没有 errorMessage 字段：解码后字段为空，不报错
func TestCodecLegacyAssistantWithoutErrorMessage(t *testing.T) {
	decoded, err := decodeMessage(json.RawMessage(`{
		"role":"assistant","content":[{"type":"text","text":"old"}],
		"api":"openai-responses","provider":"deepseek","model":"m","stopReason":"stop","usage":{}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	message := decoded.(llm.AssistantMessage)
	if message.ErrorMessage != "" || message.StopReason != llm.StopReasonStop {
		t.Fatalf("legacy decode = %+v", message)
	}
}
