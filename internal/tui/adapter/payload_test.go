package adapter

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// 队列载荷转领域消息必须保留文本与图片
func TestPendingMessageKeepsTextAndImages(t *testing.T) {
	message := pendingMessage(agent.PendingPayload{
		Text:   "look",
		Images: []agent.ImagePart{{Data: "aGk=", MIMEType: "image/png"}},
	})
	if len(message.Content) != 2 {
		t.Fatalf("content = %d, want text and image", len(message.Content))
	}
	if text, ok := message.Content[0].(llm.TextContent); !ok || text.Text != "look" {
		t.Errorf("content[0] = %#v, want the text part", message.Content[0])
	}
	if image, ok := message.Content[1].(llm.ImageContent); !ok || image.Data != "aGk=" {
		t.Errorf("content[1] = %#v, want the image part", message.Content[1])
	}

	// 只有图片时不产生空文本段
	imageOnly := pendingMessage(agent.PendingPayload{Images: []agent.ImagePart{{Data: "aGk="}}})
	if len(imageOnly.Content) != 1 {
		t.Fatalf("image-only content = %d, want 1", len(imageOnly.Content))
	}
}

// 批量转换必须跳过空载荷，保持顺序
func TestPayloadsFromMessagesSkipsEmpty(t *testing.T) {
	messages := []llm.UserMessage{
		{Content: []llm.Content{llm.TextContent{Text: "first"}}},
		{},
		{Content: []llm.Content{llm.TextContent{Text: "second"}}},
	}
	payloads := payloadsFromMessages(messages)
	if len(payloads) != 2 {
		t.Fatalf("payloads = %d, want 2 non-empty", len(payloads))
	}
	if payloads[0].Text != "first" || payloads[1].Text != "second" {
		t.Fatalf("payloads = %+v, want order preserved", payloads)
	}
	if payloadsFromMessages(nil) != nil {
		t.Error("payloadsFromMessages(nil) != nil, want nil")
	}
}

// 队列文本视图必须与载荷一致
func TestQueueHelpersAgainstApp(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.ShutdownSubagents()
	if err := a.Enqueue(agent.QueueSteering, agent.PendingPayload{Text: "queued"}); err != nil {
		t.Fatal(err)
	}

	messages := queueUserMessages(a, agent.QueueSteering)
	if len(messages) != 1 {
		t.Fatalf("queueUserMessages() = %d, want 1", len(messages))
	}

	// queueUserMessagesFrom 会取空队列，因此放在读取之后
	if got := queueUserMessagesFrom(a, agent.QueueSteering); len(got) != 1 {
		t.Fatalf("queueUserMessagesFrom() = %d, want 1", len(got))
	}
	if got := a.QueueLen(agent.QueueSteering); got != 0 {
		t.Fatalf("QueueLen() = %d after take, want 0", got)
	}

	// queueTexts 同样取空队列，单独再入队一次
	if err := a.Enqueue(agent.QueueSteering, agent.PendingPayload{Text: "queued"}); err != nil {
		t.Fatal(err)
	}
	texts := queueTexts(a, agent.QueueSteering)
	if len(texts) != 1 || texts[0] != "queued" {
		t.Fatalf("queueTexts() = %v, want [queued]", texts)
	}
}

// 编辑器载荷必须能还原文本与图片
func TestPayloadFromMessageRoundTrip(t *testing.T) {
	message := &llm.UserMessage{Content: []llm.Content{
		llm.TextContent{Text: "describe "},
		llm.ImageContent{Data: "aGk=", MIMEType: "image/png"},
	}}
	payload := payloadFromMessage(message)
	if payload == nil || payload.Text != "describe " || len(payload.Images) != 1 {
		t.Fatalf("payloadFromMessage() = %+v, want text and one image", payload)
	}
	if payload.Images[0] != (tui.ImagePart{Data: "aGk=", MIMEType: "image/png"}) {
		t.Errorf("image = %+v, want the original bytes", payload.Images[0])
	}
}
