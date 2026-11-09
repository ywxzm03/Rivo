package adapter

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// 编辑器产出与队列撤回走同一条转换，空白裁剪必须一致
func TestPayloadMessageMatchesQueueConversion(t *testing.T) {
	payload := &tui.ComposerPayload{Text: "  hello  "}
	fromEditor := payloadMessage(payload)
	if fromEditor == nil {
		t.Fatal("payloadMessage returned nil")
	}
	fromQueue := agent.PendingPayload{Text: payload.Text}.UserMessage()
	if len(fromEditor.Content) != len(fromQueue.Content) {
		t.Fatalf("content len editor=%d queue=%d", len(fromEditor.Content), len(fromQueue.Content))
	}
	editorText, ok := fromEditor.Content[0].(llm.TextContent)
	if !ok {
		t.Fatalf("editor content = %T", fromEditor.Content[0])
	}
	queueText, ok := fromQueue.Content[0].(llm.TextContent)
	if !ok {
		t.Fatalf("queue content = %T", fromQueue.Content[0])
	}
	if editorText.Text != queueText.Text {
		t.Fatalf("editor=%q queue=%q", editorText.Text, queueText.Text)
	}
}

// 只有空白时两条路径都必须判为空消息
func TestPayloadMessageTreatsBlankAsEmpty(t *testing.T) {
	if got := payloadMessage(&tui.ComposerPayload{Text: "   "}); got != nil {
		t.Fatalf("blank payload = %#v, want nil", got)
	}
}
