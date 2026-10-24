package agenttool

import (
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const forkPlaceholder = "Fork started — processing in background"

func buildForkMessages(parent []llm.Message, directive, worktreePath, parentCwd string) []llm.Message {
	messages := cloneMessages(parent)
	lastAssistant := -1
	var calls []llm.ToolCall
	for i := len(messages) - 1; i >= 0; i-- {
		assistant, ok := messages[i].(llm.AssistantMessage)
		if !ok {
			continue
		}
		lastAssistant = i
		for _, content := range assistant.Content {
			if call, ok := content.(llm.ToolCall); ok {
				calls = append(calls, call)
			}
		}
		break
	}
	if lastAssistant >= 0 && len(calls) > 0 {
		existingResults := make(map[string]llm.ToolResultMessage)
		remaining := make([]llm.Message, 0, len(messages)-lastAssistant-1)
		callIDs := make(map[string]bool, len(calls))
		for _, call := range calls {
			callIDs[call.ID] = true
		}
		for _, message := range messages[lastAssistant+1:] {
			if result, ok := message.(llm.ToolResultMessage); ok && callIDs[result.ToolCallID] {
				existingResults[result.ToolCallID] = result
				continue
			}
			remaining = append(remaining, message)
		}
		ordered := make([]llm.Message, 0, len(calls)+len(remaining))
		for _, call := range calls {
			if result, ok := existingResults[call.ID]; ok {
				ordered = append(ordered, result)
				continue
			}
			ordered = append(ordered, llm.ToolResultMessage{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content:    []llm.Content{llm.TextContent{Text: forkPlaceholder}},
			})
		}
		messages = append(messages[:lastAssistant+1], append(ordered, remaining...)...)
	}
	text := "Your directive: " + strings.TrimSpace(directive)
	if worktreePath != "" {
		text += "\n\nThe task runs in worktree " + worktreePath + ". Translate inherited paths from " + parentCwd + " to the worktree root and reread files before editing."
	}
	return append(messages, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}})
}

func cloneMessages(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		switch message := message.(type) {
		case llm.UserMessage:
			message.Content = llm.CloneContent(message.Content)
			result = append(result, message)
		case llm.AssistantMessage:
			message.Content = llm.CloneContent(message.Content)
			result = append(result, message)
		case llm.ToolResultMessage:
			message.Content = llm.CloneContent(message.Content)
			result = append(result, message)
		default:
			result = append(result, message)
		}
	}
	return result
}
