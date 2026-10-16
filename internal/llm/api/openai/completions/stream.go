package completions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type Client struct{ api sdk.Client }

type toolState struct {
	contentIndex int
	call         llm.ToolCall
	arguments    string
}

// NewClient 创建 Chat Completions 客户端
func NewClient(apiKey, baseURL string) *Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Client{api: sdk.NewClient(opts...)}
}

// Stream 将 Chat Completions SSE 事件转换为统一模型事件
func (cl *Client) Stream(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
	if model.API != llm.APIOpenAICompletions {
		return nil, fmt.Errorf("openai: protocol %q not supported", model.API)
	}
	params, err := BuildParams(model, c, opts)
	if err != nil {
		return nil, err
	}
	var requestOptions []option.RequestOption
	if model.Provider == "openai" && opts.SessionID != "" {
		requestOptions = append(requestOptions,
			option.WithHeader("session_id", opts.SessionID),
			option.WithHeader("x-client-request-id", opts.SessionID),
			option.WithHeader("x-session-affinity", opts.SessionID),
		)
	}
	stream := cl.api.Chat.Completions.NewStreaming(ctx, params, requestOptions...)
	events := make(chan llm.AssistantMessageEvent)
	go func() {
		defer close(events)
		defer stream.Close()
		partial := &llm.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID}
		if !sendEvent(ctx, events, llm.AssistantMessageStartEvent{Partial: snapshot(partial)}) {
			sendTerminal(events, llm.AssistantMessageErrorEvent{Reason: llm.StopReasonAborted, Err: context.Canceled, Partial: snapshot(partial)})
			return
		}
		var textIndex = -1
		var thinkingIndex = -1
		tools := make(map[int64]*toolState)
		terminal := false
		send := func(event llm.AssistantMessageEvent) bool {
			return sendEvent(ctx, events, withPartial(event, snapshot(partial)))
		}
		for stream.Next() {
			chunk := stream.Current()
			partial.ResponseID = chunk.ID
			if chunk.Model != "" && chunk.Model != model.ID {
				partial.Model = chunk.Model
			}
			if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
				partial.Usage = usageFromChunk(chunk)
			}
			raw := map[string]any{}
			_ = json.Unmarshal([]byte(chunk.RawJSON()), &raw)
			choice, ok := firstChoice(chunk)
			if !ok {
				continue
			}
			if choice.FinishReason != "" {
				partial.StopReason = mapStopReason(choice.FinishReason)
				terminal = true
			}
			if choice.Delta.Content != "" || choice.Delta.Refusal != "" {
				if textIndex < 0 {
					textIndex = len(partial.Content)
					partial.Content = append(partial.Content, llm.TextContent{})
					if !send(llm.AssistantMessageTextStartEvent{ContentIndex: textIndex}) {
						return
					}
				}
				delta := choice.Delta.Content
				if delta == "" {
					delta = choice.Delta.Refusal
				}
				appendText(partial, textIndex, delta)
				if !send(llm.AssistantMessageTextDeltaEvent{ContentIndex: textIndex, Delta: delta}) {
					return
				}
			}
			for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
				delta := rawDeltaString(raw, field)
				if delta == "" {
					continue
				}
				if thinkingIndex < 0 {
					thinkingIndex = len(partial.Content)
					partial.Content = append(partial.Content, llm.ThinkingContent{ThinkingSignature: field})
					if !send(llm.AssistantMessageThinkingStartEvent{ContentIndex: thinkingIndex}) {
						return
					}
				}
				appendThinking(partial, thinkingIndex, delta)
				if !send(llm.AssistantMessageThinkingDeltaEvent{ContentIndex: thinkingIndex, Delta: delta}) {
					return
				}
				break
			}
			for _, delta := range choice.Delta.ToolCalls {
				state := tools[delta.Index]
				if state == nil {
					state = &toolState{contentIndex: len(partial.Content)}
					state.call = llm.ToolCall{ID: delta.ID, Arguments: map[string]any{}}
					partial.Content = append(partial.Content, state.call)
					tools[delta.Index] = state
					if !send(llm.AssistantMessageToolCallStartEvent{ContentIndex: state.contentIndex, ToolCall: state.call}) {
						return
					}
				}
				if delta.ID != "" {
					state.call.ID = delta.ID
				}
				if delta.Function.Name != "" {
					state.call.Name = delta.Function.Name
				}
				state.arguments += delta.Function.Arguments
				state.call.Arguments = decodeArguments(state.arguments)
				partial.Content[state.contentIndex] = state.call
				if delta.Function.Arguments != "" {
					if !send(llm.AssistantMessageToolCallDeltaEvent{ContentIndex: state.contentIndex, Delta: delta.Function.Arguments}) {
						return
					}
				}
			}
		}
		if err := stream.Err(); err != nil {
			sendTerminal(events, llm.AssistantMessageErrorEvent{Reason: classifyReason(ctx), Err: err, Partial: snapshot(partial)})
			return
		}
		for _, state := range tools {
			partial.Content[state.contentIndex] = state.call
			if !send(llm.AssistantMessageToolCallEndEvent{ContentIndex: state.contentIndex, ToolCall: state.call}) {
				return
			}
		}
		if textIndex >= 0 {
			if !send(llm.AssistantMessageTextEndEvent{ContentIndex: textIndex, Content: textContent(partial, textIndex)}) {
				return
			}
		}
		if thinkingIndex >= 0 {
			if !send(llm.AssistantMessageThinkingEndEvent{ContentIndex: thinkingIndex, Content: thinkingText(partial, thinkingIndex)}) {
				return
			}
		}
		if ctx.Err() != nil {
			sendTerminal(events, llm.AssistantMessageErrorEvent{Reason: llm.StopReasonAborted, Err: ctx.Err(), Partial: snapshot(partial)})
			return
		}
		if !terminal {
			partial.StopReason = llm.StopReasonStop
		}
		if len(tools) > 0 {
			partial.StopReason = llm.StopReasonToolUse
		}
		if partial.StopReason == llm.StopReasonError {
			sendTerminal(events, llm.AssistantMessageErrorEvent{Reason: llm.StopReasonError, Err: errors.New("openai: provider rejected the completion"), Partial: snapshot(partial)})
			return
		}
		sendTerminal(events, llm.AssistantMessageDoneEvent{Message: snapshot(partial), Partial: snapshot(partial)})
	}()
	return events, nil
}

func firstChoice(chunk sdk.ChatCompletionChunk) (sdk.ChatCompletionChunkChoice, bool) {
	if len(chunk.Choices) == 0 {
		return sdk.ChatCompletionChunkChoice{}, false
	}
	return chunk.Choices[0], true
}

func usageFromChunk(chunk sdk.ChatCompletionChunk) llm.Usage {
	read := int(chunk.Usage.PromptTokensDetails.CachedTokens)
	write := int(chunk.Usage.PromptTokensDetails.CacheWriteTokens)
	input := int(chunk.Usage.PromptTokens) - read - write
	if input < 0 {
		input = 0
	}
	return llm.Usage{InputTokens: input, OutputTokens: int(chunk.Usage.CompletionTokens)}
}

func rawDeltaString(raw map[string]any, field string) string {
	choices, _ := raw["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	value, _ := delta[field].(string)
	return value
}

func mapStopReason(reason string) llm.StopReason {
	switch reason {
	case "tool_calls", "function_call":
		return llm.StopReasonToolUse
	case "length":
		return llm.StopReasonLength
	case "content_filter":
		return llm.StopReasonError
	default:
		return llm.StopReasonStop
	}
}

func classifyReason(ctx context.Context) llm.StopReason {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return llm.StopReasonAborted
	}
	return llm.StopReasonError
}

func decodeArguments(raw string) map[string]any {
	args := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &args)
	}
	return args
}

func appendText(message *llm.AssistantMessage, index int, delta string) {
	text := message.Content[index].(llm.TextContent)
	text.Text += delta
	message.Content[index] = text
}

func appendThinking(message *llm.AssistantMessage, index int, delta string) {
	thinking := message.Content[index].(llm.ThinkingContent)
	thinking.Thinking += delta
	message.Content[index] = thinking
}

func textContent(message *llm.AssistantMessage, index int) string {
	return message.Content[index].(llm.TextContent).Text
}
func thinkingText(message *llm.AssistantMessage, index int) string {
	return message.Content[index].(llm.ThinkingContent).Thinking
}

func snapshot(message *llm.AssistantMessage) *llm.AssistantMessage {
	copy := *message
	copy.Content = append([]llm.Content(nil), message.Content...)
	return &copy
}

func sendEvent(ctx context.Context, events chan<- llm.AssistantMessageEvent, event llm.AssistantMessageEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
func sendTerminal(events chan<- llm.AssistantMessageEvent, event llm.AssistantMessageEvent) {
	events <- event
}

func withPartial(event llm.AssistantMessageEvent, partial *llm.AssistantMessage) llm.AssistantMessageEvent {
	switch event := event.(type) {
	case llm.AssistantMessageTextStartEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageTextDeltaEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageTextEndEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageThinkingStartEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageThinkingDeltaEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageThinkingEndEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageToolCallStartEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageToolCallDeltaEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageToolCallEndEvent:
		event.Partial = partial
		return event
	}
	return event
}
