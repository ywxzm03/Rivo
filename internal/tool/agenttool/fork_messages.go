package agenttool

import (
	"github.com/ywxzm03/Rivo/internal/llm"
)

func cloneMessages(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		switch message := message.(type) {
		case llm.UserMessage:
			message.Content = cloneContent(message.Content)
			result = append(result, message)
		case llm.AssistantMessage:
			message.Content = cloneContent(message.Content)
			result = append(result, message)
		case llm.ToolResultMessage:
			message.Content = cloneContent(message.Content)
			result = append(result, message)
		default:
			result = append(result, message)
		}
	}
	return result
}

func cloneContent(content []llm.Content) []llm.Content {
	result := make([]llm.Content, len(content))
	for i, item := range content {
		if call, ok := item.(llm.ToolCall); ok {
			copy := call
			copy.Arguments = make(map[string]any, len(call.Arguments))
			for key, value := range call.Arguments {
				copy.Arguments[key] = value
			}
			result[i] = copy
			continue
		}
		result[i] = item
	}
	return result
}
