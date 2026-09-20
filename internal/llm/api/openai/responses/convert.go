package responses

import (
	"encoding/json"
	"fmt"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func BuildParams(model llm.Model, c llm.Context, _ llm.Options) (responses.ResponseNewParams, error) {
	items, err := BuildInputItems(c)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	params := responses.ResponseNewParams{
		Model: model.ID,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items},
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
			return nil, fmt.Errorf("openai: 不支持的消息类型 %T", m)
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
		case llm.ToolCall:
			flush()
			args, err := json.Marshal(v.Arguments)
			if err != nil {
				return nil, fmt.Errorf("openai: 序列化工具 %q 的参数: %w", v.Name, err)
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCall(
				string(args), v.ID, v.Name,
			))
		}
	}
	flush()
	return items, nil
}

func assistantMessageID(msgIndex, textIndex int) string {
	if textIndex == 0 {
		return fmt.Sprintf("msg_rivo_%d", msgIndex)
	}
	return fmt.Sprintf("msg_rivo_%d_%d", msgIndex, textIndex)
}

func toolResultItem(m llm.ToolResultMessage) responses.ResponseInputItemUnionParam {
	var text string
	for _, c := range m.Content {
		if v, ok := c.(llm.TextContent); ok {
			text += v.Text
		}
	}
	return responses.ResponseInputItemUnionParam{
		OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: sdk.String(m.ToolCallID),
			Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfString: sdk.String(text),
			},
		},
	}
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

func BuildAssistantMessage(resp responses.Response) (*llm.AssistantMessage, []llm.ToolCall, error) {
	msg := &llm.AssistantMessage{}
	var calls []llm.ToolCall
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					msg.Content = append(msg.Content, llm.TextContent{Text: part.Text})
				}
			}
		case "function_call":
			tc, err := toolCallFromItem(item)
			if err != nil {
				return nil, nil, err
			}
			calls = append(calls, tc)
			msg.Content = append(msg.Content, tc)
		}

	}
	switch {
	case len(calls) > 0:
		msg.StopReason = llm.StopReasonToolUse
	case resp.Status == responses.ResponseStatusIncomplete &&
		resp.IncompleteDetails.Reason == "max_output_tokens":
		msg.StopReason = llm.StopReasonLength
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
			return llm.ToolCall{}, fmt.Errorf("openai: 工具 %q 的参数不是合法 JSON: %w", item.Name, err)
		}
	}
	return llm.ToolCall{ID: item.CallID, Name: item.Name, Arguments: args}, nil
}
