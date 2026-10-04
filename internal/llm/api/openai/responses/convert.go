package responses

import (
	"encoding/json"
	"fmt"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// BuildParams 将 Rivo 上下文和模型配置转换为 Responses 请求参数
func BuildParams(model llm.Model, c llm.Context, opts llm.Options) (responses.ResponseNewParams, error) {
	items, err := BuildInputItems(c)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	params := responses.ResponseNewParams{
		Model: model.ID,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items},
	}
	if opts.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(opts.MaxTokens))
	}
	if model.Reasoning {
		effort := model.ReasoningEffort
		if effort == "" {
			effort = llm.ReasoningEffortMedium
		}
		if !validReasoningEffort(effort) {
			return responses.ResponseNewParams{}, fmt.Errorf("openai: unsupported reasoning effort %q", effort)
		}
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(effort),
			Summary: shared.ReasoningSummaryAuto,
		}
		params.Include = append(params.Include, responses.ResponseIncludableReasoningEncryptedContent)
	}
	for _, t := range c.Tools {
		params.Tools = append(params.Tools, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        t.Name(),
				Description: sdk.String(t.Description()),
				Parameters:  t.Parameters(),
			},
		})
	}
	return params, nil
}

// validReasoningEffort 判断 reasoning effort 是否为支持的等级
func validReasoningEffort(effort llm.ReasoningEffort) bool {
	switch effort {
	case llm.ReasoningEffortLow, llm.ReasoningEffortMedium, llm.ReasoningEffortHigh,
		llm.ReasoningEffortXHigh, llm.ReasoningEffortMax:
		return true
	default:
		return false
	}
}

func BuildInputItems(c llm.Context) (responses.ResponseInputParam, error) {
	var items responses.ResponseInputParam
	if c.SystemPrompt != "" {
		items = append(items, responses.ResponseInputItemParamOfMessage(
			c.SystemPrompt, responses.EasyInputMessageRoleSystem,
		))
	}
	for i, m := range c.Messages {
		switch msg := m.(type) {
		case llm.UserMessage:
			items = append(items, userMessageItem(msg))
		case llm.AssistantMessage:
			assistantItems, err := assistantMessageItems(i, msg)
			if err != nil {
				return nil, err
			}
			items = append(items, assistantItems...)
		case llm.ToolResultMessage:
			items = append(items, toolResultItem(msg))
		default:
			return nil, fmt.Errorf("openai: unsupported message type %T", m)
		}
	}
	return items, nil
}

func userMessageItem(m llm.UserMessage) responses.ResponseInputItemUnionParam {
	contents := make(responses.ResponseInputMessageContentListParam, 0, len(m.Content))
	for _, c := range m.Content {
		switch v := c.(type) {
		case llm.TextContent:
			contents = append(contents, textContent(v.Text))
		case llm.ImageContent:
			contents = append(contents, imageContent(v))
		}
	}
	return responses.ResponseInputItemParamOfMessage(contents, responses.EasyInputMessageRoleUser)
}

// assistantMessageItems 将 assistant 内容拆分为 Responses 输入 item
func assistantMessageItems(msgIndex int, m llm.AssistantMessage) ([]responses.ResponseInputItemUnionParam, error) {
	var items []responses.ResponseInputItemUnionParam
	var contents []responses.ResponseOutputMessageContentUnionParam
	textIndex := 0
	flush := func() {
		if len(contents) == 0 {
			return
		}
		items = append(items, responses.ResponseInputItemParamOfOutputMessage(
			contents, assistantMessageID(msgIndex, textIndex), responses.ResponseOutputMessageStatusCompleted,
		))
		textIndex++
		contents = nil
	}
	for _, c := range m.Content {
		switch v := c.(type) {
		case llm.TextContent:
			contents = append(contents, outputTextContent(v.Text))
		case llm.ThinkingContent:
			flush()
			if v.ThinkingSignature == "" {
				continue
			}
			item, err := reasoningItemParam(v)
			if err != nil {
				return nil, err
			}
			items = append(items, responses.ResponseInputItemUnionParam{OfReasoning: &item})
		case llm.ToolCall:
			flush()
			args, err := json.Marshal(v.Arguments)
			if err != nil {
				return nil, fmt.Errorf("openai: marshal tool %q arguments: %w", v.Name, err)
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCall(
				string(args), v.ID, v.Name,
			))
		}
	}
	flush()
	return items, nil
}

// reasoningItemParam 将持久化的 provider reasoning item 转回 SDK 参数
func reasoningItemParam(content llm.ThinkingContent) (responses.ResponseReasoningItemParam, error) {
	var item responses.ResponseReasoningItem
	if err := json.Unmarshal([]byte(content.ThinkingSignature), &item); err != nil {
		return responses.ResponseReasoningItemParam{}, fmt.Errorf("openai: parse reasoning item: %w", err)
	}
	return item.ToParam(), nil
}

func assistantMessageID(msgIndex, textIndex int) string {
	if textIndex == 0 {
		return fmt.Sprintf("msg_rivo_%d", msgIndex)
	}
	return fmt.Sprintf("msg_rivo_%d_%d", msgIndex, textIndex)
}

func toolResultItem(m llm.ToolResultMessage) responses.ResponseInputItemUnionParam {
	var output responses.ResponseInputItemFunctionCallOutputOutputUnionParam
	if allText(m.Content) {
		// 纯文本仍走字符串形态，线上形状不变
		var text string
		for _, c := range m.Content {
			text += c.(llm.TextContent).Text
		}
		output.OfString = sdk.String(text)
	} else {
		// 含图片时改走结构化形态，非文本内容才不会被丢掉
		var items responses.ResponseFunctionCallOutputItemListParam
		for _, c := range m.Content {
			switch v := c.(type) {
			case llm.TextContent:
				items = append(items, responses.ResponseFunctionCallOutputItemUnionParam{
					OfInputText: &responses.ResponseInputTextContentParam{Text: v.Text},
				})
			case llm.ImageContent:
				items = append(items, responses.ResponseFunctionCallOutputItemUnionParam{
					OfInputImage: &responses.ResponseInputImageContentParam{
						Detail:   responses.ResponseInputImageContentDetailAuto,
						ImageURL: sdk.String("data:" + v.MIMEType + ";base64," + v.Data),
					},
				})
			}
		}
		output.OfResponseFunctionCallOutputItemArray = items
	}
	return responses.ResponseInputItemUnionParam{
		OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: sdk.String(m.ToolCallID),
			Output: output,
		},
	}
}

// allText 判断工具结果里是不是只有文本
func allText(content []llm.Content) bool {
	for _, c := range content {
		if _, ok := c.(llm.TextContent); !ok {
			return false
		}
	}
	return true
}

func textContent(s string) responses.ResponseInputContentUnionParam {
	return responses.ResponseInputContentUnionParam{
		OfInputText: &responses.ResponseInputTextParam{Text: s},
	}
}

func outputTextContent(s string) responses.ResponseOutputMessageContentUnionParam {
	return responses.ResponseOutputMessageContentUnionParam{
		OfOutputText: &responses.ResponseOutputTextParam{
			Text:        s,
			Annotations: []responses.ResponseOutputTextAnnotationUnionParam{},
		},
	}
}

func imageContent(c llm.ImageContent) responses.ResponseInputContentUnionParam {
	return responses.ResponseInputContentUnionParam{
		OfInputImage: &responses.ResponseInputImageParam{
			Detail:   responses.ResponseInputImageDetailAuto,
			ImageURL: sdk.String("data:" + c.MIMEType + ";base64," + c.Data),
		},
	}
}

// BuildAssistantMessage 将 Responses 输出转换为 Rivo assistant 消息和工具调用
func BuildAssistantMessage(resp responses.Response) (*llm.AssistantMessage, []llm.ToolCall, error) {
	msg := &llm.AssistantMessage{ResponseID: resp.ID}
	var calls []llm.ToolCall
	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			reasoning := item.AsReasoning()
			var thinking string
			for i, part := range reasoning.Summary {
				if i > 0 {
					thinking += "\n\n"
				}
				thinking += part.Text
			}
			if thinking == "" {
				for i, part := range reasoning.Content {
					if i > 0 {
						thinking += "\n\n"
					}
					thinking += part.Text
				}
			}
			signature := reasoning.RawJSON()
			if signature == "" {
				data, err := json.Marshal(reasoning)
				if err != nil {
					return nil, nil, fmt.Errorf("openai: marshal reasoning item: %w", err)
				}
				signature = string(data)
			}
			msg.Content = append(msg.Content, llm.ThinkingContent{
				Thinking:          thinking,
				ThinkingSignature: signature,
			})
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					msg.Content = append(msg.Content, llm.TextContent{Text: part.Text})
				} else if part.Type == "refusal" {
					msg.Content = append(msg.Content, llm.TextContent{Text: part.Refusal})
				}
			}
		case "function_call":
			tc, err := toolCallFromItem(item)
			if err != nil {
				return nil, nil, err
			}
			calls = append(calls, tc)
			msg.Content = append(msg.Content, tc)
		default:
			return nil, nil, fmt.Errorf("openai: unsupported response output item type %q", item.Type)
		}

	}
	switch {
	case len(calls) > 0:
		msg.StopReason = llm.StopReasonToolUse
	case resp.Status == responses.ResponseStatusIncomplete &&
		resp.IncompleteDetails.Reason == "max_output_tokens":
		msg.StopReason = llm.StopReasonLength
	case resp.Status == responses.ResponseStatusIncomplete,
		resp.Status == responses.ResponseStatusFailed,
		resp.Status == responses.ResponseStatusCancelled:
		msg.StopReason = llm.StopReasonError
	default:
		msg.StopReason = llm.StopReasonStop
	}
	msg.Usage = llm.Usage{
		InputTokens:  int(resp.Usage.InputTokens),
		OutputTokens: int(resp.Usage.OutputTokens),
	}
	return msg, calls, nil
}

func toolCallFromItem(item responses.ResponseOutputItemUnion) (llm.ToolCall, error) {
	args := map[string]any{}
	if raw := item.Arguments.OfString; raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return llm.ToolCall{}, fmt.Errorf("openai: tool %q arguments are not valid JSON: %w", item.Name, err)
		}
	}
	return llm.ToolCall{ID: item.CallID, Name: item.Name, Arguments: args}, nil
}
