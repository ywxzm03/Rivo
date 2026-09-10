package agent

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type Agent struct {
	Model llm.Model
}

func (a *Agent) AgentLoop(ctx context.Context, prompt string) {
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
	input := responses.ResponseNewParamsInputUnion{
		OfString: openai.String(prompt),
	}

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
