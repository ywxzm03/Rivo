package openai

import (
	"context"
	"fmt"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type Client struct {
	api sdk.Client
}

func NewClient(apiKey, baseURL string) *Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Client{api: sdk.NewClient(opts...)}
}

func (cl *Client) Stream(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.Event, error) {
	if model.API != llm.APIOpenAIResponses {
		return nil, fmt.Errorf("openai: 暂不支持协议 %q", model.API)
	}
	params, err := BuildParams(model, c, opts)
	if err != nil {
		return nil, err
	}
	stream := cl.api.Responses.NewStreaming(ctx, params)

	events := make(chan llm.Event)
	go func() {
		defer close(events)
		defer stream.Close()
		send := func(e llm.Event) bool {
			select {
			case events <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for stream.Next() {
			ev := stream.Current()
			switch ev.Type {
			case "response.output_text.delta":
				if !send(llm.Event{Type: llm.EventTextDelta, Delta: ev.Delta}) {
					return
				}
			case "response.completed", "response.incomplete":
				msg, calls, err := BuildAssistantMessage(ev.Response)
				if err != nil {
					send(llm.Event{Type: llm.EventError, Err: err})
					return
				}
				for _, tc := range calls {
					if !send(llm.Event{Type: llm.EventToolCall, ToolCall: tc}) {
						return
					}
				}
				send(llm.Event{Type: llm.EventDone, Message: msg})
				return
			case "response.failed":
				errResp := ev.Response.Error
				send(llm.Event{Type: llm.EventError, Err: apiError("openai: 请求失败", string(errResp.Code), errResp.Message)})
				return
			case "error":
				send(llm.Event{Type: llm.EventError, Err: apiError("openai", ev.Code, ev.Message)})
				return
			}
		}
		if err := stream.Err(); err != nil {
			send(llm.Event{Type: llm.EventError, Err: fmt.Errorf("openai: 流中断: %w", err)})
		}
	}()
	return events, nil
}

func apiError(prefix, code, message string) error {
	if code != "" {
		return fmt.Errorf("%s: %s: %s", prefix, code, message)
	}
	return fmt.Errorf("%s: %s", prefix, message)
}
