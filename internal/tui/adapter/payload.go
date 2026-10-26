package adapter

import (
	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// payloadMessage 把编辑器产出转成领域消息；图片按 base64 原样搬运
func payloadMessage(payload *tui.ComposerPayload) *llm.UserMessage {
	if payload == nil {
		return nil
	}
	content := make([]llm.Content, 0, len(payload.Images)+1)
	if payload.Text != "" {
		content = append(content, llm.TextContent{Text: payload.Text})
	}
	for _, image := range payload.Images {
		content = append(content, llm.ImageContent{Data: image.Data, MIMEType: image.MIMEType})
	}
	if len(content) == 0 {
		return nil
	}
	return &llm.UserMessage{Content: content}
}

// payloadFromMessage 是 payloadMessage 的逆变换，用于把已入队的消息回填到编辑器
func payloadFromMessage(message *llm.UserMessage) *tui.ComposerPayload {
	if message == nil {
		return nil
	}
	payload := &tui.ComposerPayload{}
	for _, content := range message.Content {
		switch content := content.(type) {
		case llm.TextContent:
			payload.Text += content.Text
		case llm.ImageContent:
			payload.Images = append(payload.Images, tui.ImagePart{Data: content.Data, MIMEType: content.MIMEType})
		}
	}
	if payload.Text == "" && len(payload.Images) == 0 {
		return nil
	}
	return payload
}

// restoreMode 把 TUI 的恢复方式映射到领域层的恢复模式
func restoreMode(kind tui.RestoreKind) filehistory.RestoreMode {
	switch kind {
	case tui.RestoreCode:
		return filehistory.RestoreCode
	case tui.RestoreCodeAndContext:
		return filehistory.RestoreCodeAndContext
	default:
		return filehistory.RestoreContext
	}
}

// payloadsFromMessages 批量把已入队消息转成编辑器载荷
func payloadsFromMessages(messages []llm.UserMessage) []tui.ComposerPayload {
	if len(messages) == 0 {
		return nil
	}
	payloads := make([]tui.ComposerPayload, 0, len(messages))
	for i := range messages {
		if payload := payloadFromMessage(&messages[i]); payload != nil {
			payloads = append(payloads, *payload)
		}
	}
	return payloads
}

// pendingMessage 把队列内容转成领域消息，保留图片
func pendingMessage(payload agent.PendingPayload) llm.UserMessage {
	content := make([]llm.Content, 0, len(payload.Images)+1)
	if payload.Text != "" {
		content = append(content, llm.TextContent{Text: payload.Text})
	}
	for _, image := range payload.Images {
		content = append(content, llm.ImageContent{Data: image.Data, MIMEType: image.MIMEType})
	}
	return llm.UserMessage{Content: content}
}

// queueUserMessages 把队列内容转成领域消息，供既有投影逻辑复用
func queueUserMessages(app *app.App, kind agent.QueueKind) []llm.UserMessage {
	payloads := app.QueuePayloads(kind)
	messages := make([]llm.UserMessage, 0, len(payloads))
	for _, payload := range payloads {
		messages = append(messages, pendingMessage(payload))
	}
	return messages
}

// queueUserMessagesFrom 取出并清空队列，返回领域消息
func queueUserMessagesFrom(app *app.App, kind agent.QueueKind) []llm.UserMessage {
	payloads := app.TakeQueue(kind)
	messages := make([]llm.UserMessage, 0, len(payloads))
	for _, payload := range payloads {
		messages = append(messages, pendingMessage(payload))
	}
	return messages
}

// queueTexts 取出并清空队列，返回纯文本
func queueTexts(app *app.App, kind agent.QueueKind) []string {
	payloads := app.TakeQueue(kind)
	texts := make([]string, len(payloads))
	for i, payload := range payloads {
		texts[i] = payload.Text
	}
	return texts
}
