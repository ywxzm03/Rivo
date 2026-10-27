package completions

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/api/openai/cache"
)

// BuildParams 将 Rivo 上下文和模型配置转换为 Chat Completions 请求参数
func BuildParams(model llm.Model, c llm.Context, opts llm.Options) (sdk.ChatCompletionNewParams, error) {
	messages, err := convertMessages(model, c)
	if err != nil {
		return sdk.ChatCompletionNewParams{}, err
	}
	body := map[string]any{
		"model":          model.ID,
		"messages":       messages,
		"stream":         true,
		"store":          false,
		"stream_options": map[string]any{"include_usage": true},
	}
	if cache.SupportsPromptCacheOptions(model) {
		body["prompt_cache_options"] = map[string]any{"ttl": cache.PromptCacheTTL}
		if opts.SessionID != "" {
			body["prompt_cache_key"] = cache.PromptCacheKey(opts.SessionID)
		}
	}
	if len(c.Tools) > 0 {
		body["tools"] = buildTools(c.Tools)
	}
	if opts.MaxTokens > 0 {
		if model.Provider == "deepseek" {
			body["max_tokens"] = opts.MaxTokens
		} else {
			body["max_completion_tokens"] = opts.MaxTokens
		}
	}
	// overrides 收集 SDK 结构体未建模的键（thinking、自定义采样参数），
	// 它们既写进请求体，也要通过 SetExtraFields 补回被 json 丢弃的部分
	overrides := make(map[string]any)
	if model.Reasoning {
		if model.Provider == "deepseek" {
			overrides["thinking"] = map[string]any{"type": thinkingMode(opts)}
		} else {
			body["reasoning_effort"] = string(resolveReasoningEffort(model, opts))
		}
	}
	for key, value := range model.SamplingParams {
		if !reservedParamKeys[key] {
			overrides[key] = value
		}
	}
	maps.Copy(body, overrides)
	data, err := json.Marshal(body)
	if err != nil {
		return sdk.ChatCompletionNewParams{}, fmt.Errorf("openai: marshal chat completion params: %w", err)
	}
	var params sdk.ChatCompletionNewParams
	if err := json.Unmarshal(data, &params); err != nil {
		return sdk.ChatCompletionNewParams{}, fmt.Errorf("openai: build chat completion params: %w", err)
	}
	if len(overrides) > 0 {
		params.SetExtraFields(overrides)
	}
	return params, nil
}

// thinkingMode 返回 DeepSeek thinking 字段的取值
func thinkingMode(opts llm.Options) string {
	if thinkingEnabled(opts) {
		return "enabled"
	}
	return "disabled"
}

var reservedParamKeys = map[string]bool{
	"model": true, "messages": true, "stream": true, "max_tokens": true,
	"max_completion_tokens": true, "store": true, "tools": true, "tool_choice": true,
	"reasoning_effort": true, "thinking": true, "prompt_cache_key": true,
	"prompt_cache_options": true, "prompt_cache_retention": true,
}

func convertMessages(model llm.Model, c llm.Context) ([]any, error) {
	messages := make([]any, 0, len(c.Messages)+1)
	if c.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": instructionRole(model), "content": c.SystemPrompt})
	}
	for i, message := range c.Messages {
		switch m := message.(type) {
		case llm.UserMessage:
			content, err := userContent(m.Content)
			if err != nil {
				return nil, fmt.Errorf("openai: user message %d: %w", i, err)
			}
			messages = append(messages, map[string]any{"role": "user", "content": content})
		case llm.AssistantMessage:
			messages = append(messages, assistantMessage(model, m))
		case llm.ToolResultMessage:
			messages = append(messages, toolResultMessage(m))
		case llm.SystemMessage:
			text := llm.RenderSystemMessageUpdate(m)
			if text != "" {
				messages = append(messages, map[string]any{"role": instructionRole(model), "content": text})
			}
		default:
			return nil, fmt.Errorf("openai: unsupported message type %T", message)
		}
	}
	return messages, nil
}

func instructionRole(model llm.Model) string {
	if model.Provider == "openai" && model.Reasoning {
		return "developer"
	}
	return "system"
}

func userContent(content []llm.Content) (any, error) {
	if len(content) == 1 {
		if text, ok := content[0].(llm.TextContent); ok {
			return text.Text, nil
		}
	}
	parts := make([]any, 0, len(content))
	for _, item := range content {
		switch v := item.(type) {
		case llm.TextContent:
			parts = append(parts, map[string]any{"type": "text", "text": v.Text})
		case llm.ImageContent:
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + v.MIMEType + ";base64," + v.Data,
				},
			})
		default:
			return nil, fmt.Errorf("unsupported user content %T", item)
		}
	}
	return parts, nil
}

func assistantMessage(model llm.Model, message llm.AssistantMessage) map[string]any {
	textParts := make([]string, 0)
	toolCalls := make([]any, 0)
	var thinking strings.Builder
	thinkingField := "reasoning_content"
	for _, item := range message.Content {
		switch v := item.(type) {
		case llm.TextContent:
			textParts = append(textParts, v.Text)
		case llm.ThinkingContent:
			if thinking.Len() > 0 {
				thinking.WriteString("\n")
			}
			thinking.WriteString(v.Thinking)
			if v.ThinkingSignature == "reasoning" || v.ThinkingSignature == "reasoning_text" || v.ThinkingSignature == "reasoning_content" {
				thinkingField = v.ThinkingSignature
			}
		case llm.ToolCall:
			args, _ := json.Marshal(v.Arguments)
			toolCalls = append(toolCalls, map[string]any{
				"id":       v.ID,
				"type":     "function",
				"function": map[string]any{"name": v.Name, "arguments": string(args)},
			})
		}
	}
	result := map[string]any{"role": "assistant"}
	if len(textParts) > 0 {
		result["content"] = strings.Join(textParts, "")
	}
	if len(toolCalls) > 0 {
		result["tool_calls"] = toolCalls
	}
	if thinking.Len() > 0 {
		if model.Provider == "deepseek" || model.Provider == "openai" {
			result[thinkingField] = thinking.String()
		}
	}
	return result
}

func toolResultMessage(message llm.ToolResultMessage) map[string]any {
	var text strings.Builder
	for _, item := range message.Content {
		if v, ok := item.(llm.TextContent); ok {
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(v.Text)
		}
	}
	if text.Len() == 0 {
		text.WriteString("(no tool output)")
	}
	return map[string]any{"role": "tool", "tool_call_id": message.ToolCallID, "content": text.String()}
}

func thinkingEnabled(opts llm.Options) bool {
	return opts.ThinkingLevel != llm.ThinkingLevelOff
}

func resolveReasoningEffort(model llm.Model, opts llm.Options) llm.ReasoningEffort {
	if opts.ThinkingLevel == "" {
		if model.ReasoningEffort != "" {
			return model.ReasoningEffort
		}
		return llm.ReasoningEffortMedium
	}
	if mapped, ok := model.ThinkingLevelMap[opts.ThinkingLevel]; ok && mapped != nil {
		return *mapped
	}
	if opts.ThinkingLevel == llm.ThinkingLevelOff {
		return llm.ReasoningEffort("none")
	}
	return llm.ReasoningEffort(opts.ThinkingLevel)
}

func buildTool(t llm.Tool) any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"parameters":  t.Parameters(),
		},
	}
}

func buildTools(tools []llm.Tool) []any {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, buildTool(tool))
	}
	return result
}
