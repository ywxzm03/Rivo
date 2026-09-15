package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type Agent struct {
	Model llm.Model
}

func (a *Agent) AgentLoop(ctx context.Context, msg llm.AgentContext) {
	client := openai.NewClient()

	// 1.组装上下文与工具
	emptyParams := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	tools := []responses.ToolUnionParam{
		{OfFunction: &responses.FunctionToolParam{
			Name:        "hello_world",
			Description: openai.String("Print Hello World"),
			Parameters:  emptyParams,
		}},
		{OfFunction: &responses.FunctionToolParam{
			Name:        "ping",
			Description: openai.String("Return pong"),
			Parameters:  emptyParams,
		}},
		{OfFunction: &responses.FunctionToolParam{
			Name:        "now",
			Description: openai.String("Return the current time"),
			Parameters:  emptyParams,
		}},
	}

	input := a.BuildInput(msg)

	// 2.开始正式loop
	var resp responses.Response
	for {
		params := responses.ResponseNewParams{
			Model: a.Model.Name,
			Input: input,
			Tools: tools,
		}
		if resp.ID != "" {
			params.PreviousResponseID = openai.String(resp.ID)
		}

		// 3.发起流式调用
		stream := client.Responses.NewStreaming(ctx, params)
		resp = responses.Response{}
		for stream.Next() {
			event := stream.Current()
			if event.Type == "response.output_text.delta" {
				fmt.Print(event.Delta)
			}
			if event.Type == "response.completed" {
				resp = event.Response
			}
		}
		err := stream.Err()
		_ = stream.Close()
		if err != nil {
			panic(err)
		}

		// 4.开始收集工具调用信息
		var toolCall responses.ResponseInputParam
		for _, item := range resp.Output {
			if item.Type != "function_call" {
				continue
			}

			result := "unknown tool"
			// 5.工具调用
			switch item.Name {
			case "hello_world":
				result = HelloWorld()
			case "ping":
				result = Ping()
			case "now":
				result = Now()
			}
			toolCall = append(toolCall, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: openai.String(item.CallID),
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: openai.String(result),
					},
				},
			})
		}

		if len(toolCall) == 0 {
			fmt.Println()
			return
		}
		input = responses.ResponseNewParamsInputUnion{OfInputItemList: toolCall}
	}
}

func (a *Agent) BuildInput(msg llm.AgentContext) responses.ResponseNewParamsInputUnion {
	var items responses.ResponseInputParam

	// 1.System Prompt
	if msg.SystemPrompt != "" {
		items = append(items, responses.ResponseInputItemParamOfMessage(
			msg.SystemPrompt,
			responses.EasyInputMessageRoleSystem,
		))
	}

	// 2.Messages -> 根据Role进行初步分类 -> 再根据 Text/Image/ToolCall，拼接起来
	for _, m := range msg.Messages {
		switch m.Role() {
		case "user", "assistant":
			role := responses.EasyInputMessageRoleUser
			if m.Role() == "assistant" {
				role = responses.EasyInputMessageRoleAssistant
			}
			var contents responses.ResponseInputMessageContentListParam
			flush := func() {
				if len(contents) == 0 {
					return
				}
				items = append(items, responses.ResponseInputItemParamOfMessage(contents, role))
				contents = nil
			}
			for _, c := range m.GetContent() {
				switch v := c.(type) {
				case llm.TextContent:
					contents = append(contents, responses.ResponseInputContentUnionParam{
						OfInputText: &responses.ResponseInputTextParam{Text: v.Text},
					})
				case llm.ImageContent:
					contents = append(contents, responses.ResponseInputContentUnionParam{
						OfInputImage: &responses.ResponseInputImageParam{
							Detail:   responses.ResponseInputImageDetailAuto,
							ImageURL: openai.String("data:" + v.MIMEType + ";base64," + v.Data),
						},
					})
				case llm.ToolCall:
					flush()
					args, _ := json.Marshal(v.Arguments)
					items = append(items, responses.ResponseInputItemParamOfFunctionCall(
						string(args), v.ToolCallID, v.ToolName,
					))
				}
			}
			flush()
		case "tool_result":
			callID := ""
			switch t := m.(type) {
			case llm.ToolResultMessage:
				callID = t.ToolCallID
			case *llm.ToolResultMessage:
				callID = t.ToolCallID
			}
			var text string
			for _, c := range m.GetContent() {
				if v, ok := c.(llm.TextContent); ok {
					text += v.Text
				}
			}
			items = append(items, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: openai.String(callID),
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: openai.String(text),
					},
				},
			})
		}
	}

	return responses.ResponseNewParamsInputUnion{OfInputItemList: items}
}
