package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type Client struct {
	api sdk.Client
}

type outputSlot struct {
	kind       llm.EventType
	contentIdx int
	arguments  string
}

func NewClient(apiKey, baseURL string) *Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Client{api: sdk.NewClient(opts...)}
}

// Stream 将 Responses SSE 事件转换为带 content index 的统一模型事件
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
		partial := &llm.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID}
		if !sendEvent(ctx, events, llm.Event{Type: llm.EventStart, Partial: partial}) {
			return
		}
		slots := map[int64]outputSlot{}
		terminal := false
		send := func(event llm.Event) bool {
			if event.Partial == nil {
				event.Partial = assistantMessageSnapshot(partial)
			}
			return sendEvent(ctx, events, event)
		}
		for stream.Next() {
			ev := stream.Current()
			switch ev.Type {
			case "response.created":
				partial.ResponseID = ev.Response.ID
			case "response.output_item.added":
				if !addOutputSlot(partial, slots, ev.OutputIndex, ev.Item, send) {
					return
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				slot, ok := slots[ev.OutputIndex]
				if !ok || slot.kind != llm.EventThinkingStart {
					continue
				}
				appendThinking(partial, slot.contentIdx, ev.Delta)
				if !send(llm.Event{Type: llm.EventThinkingDelta, ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
					return
				}
			case "response.reasoning_summary_part.done":
				slot, ok := slots[ev.OutputIndex]
				if !ok || slot.kind != llm.EventThinkingStart {
					continue
				}
				appendThinking(partial, slot.contentIdx, "\n\n")
				if !send(llm.Event{Type: llm.EventThinkingDelta, ContentIndex: slot.contentIdx, Delta: "\n\n"}) {
					return
				}
			case "response.output_text.delta", "response.refusal.delta":
				slot, ok := slots[ev.OutputIndex]
				if !ok || slot.kind != llm.EventTextStart {
					continue
				}
				appendText(partial, slot.contentIdx, ev.Delta)
				if !send(llm.Event{Type: llm.EventTextDelta, ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
					return
				}
			case "response.function_call_arguments.delta":
				slot, ok := slots[ev.OutputIndex]
				if !ok || slot.kind != llm.EventToolCallStart {
					continue
				}
				slot.arguments += ev.Delta
				slots[ev.OutputIndex] = slot
				updateToolArguments(partial, slot.contentIdx, slot.arguments)
				if !send(llm.Event{Type: llm.EventToolCallDelta, ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
					return
				}
			case "response.function_call_arguments.done":
				slot, ok := slots[ev.OutputIndex]
				if !ok || slot.kind != llm.EventToolCallStart {
					continue
				}
				previous := slot.arguments
				slot.arguments = ev.Arguments
				slots[ev.OutputIndex] = slot
				updateToolArguments(partial, slot.contentIdx, slot.arguments)
				if strings.HasPrefix(ev.Arguments, previous) {
					delta := ev.Arguments[len(previous):]
					if delta != "" && !send(llm.Event{Type: llm.EventToolCallDelta, ContentIndex: slot.contentIdx, Delta: delta}) {
						return
					}
				}
			case "response.output_item.done":
				if !finishOutputSlot(partial, slots, ev.OutputIndex, ev.Item, send) {
					return
				}
			case "response.completed", "response.incomplete":
				terminal = true
				msg, _, err := BuildAssistantMessage(ev.Response)
				if err != nil {
					send(llm.Event{Type: llm.EventError, Err: err})
					return
				}
				if err := incompleteResponseError(ev.Response); err != nil {
					send(llm.Event{Type: llm.EventError, Err: err})
					return
				}
				if !send(llm.Event{Type: llm.EventDone, Message: msg}) {
					return
				}
				return
			case "response.failed":
				terminal = true
				errResp := ev.Response.Error
				send(llm.Event{Type: llm.EventError, Err: apiError("openai: 请求失败", string(errResp.Code), errResp.Message)})
				return
			case "error":
				terminal = true
				send(llm.Event{Type: llm.EventError, Err: apiError("openai", ev.Code, ev.Message)})
				return
			}
		}
		if !terminal {
			if err := stream.Err(); err != nil {
				send(llm.Event{Type: llm.EventError, Err: fmt.Errorf("openai: 流中断: %w", err)})
			} else {
				send(llm.Event{Type: llm.EventError, Err: errors.New("openai: 流在终止事件前结束")})
			}
		}
	}()
	return events, nil
}

// sendEvent 在上下文未取消时向模型事件通道发送事件
func sendEvent(ctx context.Context, events chan<- llm.Event, event llm.Event) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// assistantMessageSnapshot 复制消息结构和内容切片，避免事件消费者读取生产中的消息
func assistantMessageSnapshot(message *llm.AssistantMessage) *llm.AssistantMessage {
	snapshot := *message
	snapshot.Content = append([]llm.Content(nil), message.Content...)
	return &snapshot
}

// incompleteResponseError 将除输出长度限制外的 incomplete 响应转换为错误
func incompleteResponseError(response responses.Response) error {
	if response.Status != responses.ResponseStatusIncomplete || response.IncompleteDetails.Reason == "max_output_tokens" {
		return nil
	}
	if response.IncompleteDetails.Reason == "" {
		return errors.New("openai: 响应未完成")
	}
	return fmt.Errorf("openai: 响应未完成: %s", response.IncompleteDetails.Reason)
}

// addOutputSlot 为新增 output item 创建可持续更新的 assistant 内容块
func addOutputSlot(partial *llm.AssistantMessage, slots map[int64]outputSlot, outputIndex int64, item responses.ResponseOutputItemUnion, send func(llm.Event) bool) bool {
	idx := len(partial.Content)
	switch item.Type {
	case "reasoning":
		partial.Content = append(partial.Content, llm.ThinkingContent{})
		slots[outputIndex] = outputSlot{kind: llm.EventThinkingStart, contentIdx: idx}
		return send(llm.Event{Type: llm.EventThinkingStart, ContentIndex: idx})
	case "message":
		partial.Content = append(partial.Content, llm.TextContent{})
		slots[outputIndex] = outputSlot{kind: llm.EventTextStart, contentIdx: idx}
		return send(llm.Event{Type: llm.EventTextStart, ContentIndex: idx})
	case "function_call":
		call := llm.ToolCall{ID: item.CallID, Name: item.Name, Arguments: map[string]any{}}
		partial.Content = append(partial.Content, call)
		slots[outputIndex] = outputSlot{kind: llm.EventToolCallStart, contentIdx: idx, arguments: item.Arguments.OfString}
		if item.Arguments.OfString != "" {
			updateToolArguments(partial, idx, item.Arguments.OfString)
		}
		return send(llm.Event{Type: llm.EventToolCallStart, ContentIndex: idx})
	default:
		send(llm.Event{Type: llm.EventError, Err: fmt.Errorf("openai: 不支持的响应 output item 类型 %q", item.Type)})
		return false
	}
}

// finishOutputSlot 用 output_item.done 的完整 item 固化内容并发出结束事件
func finishOutputSlot(partial *llm.AssistantMessage, slots map[int64]outputSlot, outputIndex int64, item responses.ResponseOutputItemUnion, send func(llm.Event) bool) bool {
	slot, ok := slots[outputIndex]
	if !ok {
		return true
	}
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
			data, _ := json.Marshal(reasoning)
			signature = string(data)
		}
		partial.Content[slot.contentIdx] = llm.ThinkingContent{Thinking: thinking, ThinkingSignature: signature}
		delete(slots, outputIndex)
		return send(llm.Event{Type: llm.EventThinkingEnd, ContentIndex: slot.contentIdx, Content: thinking})
	case "message":
		var text string
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text += part.Text
			} else if part.Type == "refusal" {
				text += part.Refusal
			}
		}
		partial.Content[slot.contentIdx] = llm.TextContent{Text: text}
		delete(slots, outputIndex)
		return send(llm.Event{Type: llm.EventTextEnd, ContentIndex: slot.contentIdx, Content: text})
	case "function_call":
		call, err := toolCallFromItem(item)
		if err != nil {
			send(llm.Event{Type: llm.EventError, Err: err})
			return false
		}
		partial.Content[slot.contentIdx] = call
		delete(slots, outputIndex)
		return send(llm.Event{Type: llm.EventToolCallEnd, ContentIndex: slot.contentIdx, ToolCall: call})
	default:
		send(llm.Event{Type: llm.EventError, Err: fmt.Errorf("openai: 不支持的响应 output item 类型 %q", item.Type)})
		return false
	}
}

// appendThinking 将 reasoning 增量追加到指定内容块
func appendThinking(message *llm.AssistantMessage, index int, delta string) {
	content, ok := message.Content[index].(llm.ThinkingContent)
	if ok {
		content.Thinking += delta
		message.Content[index] = content
	}
}

// appendText 将文本增量追加到指定内容块
func appendText(message *llm.AssistantMessage, index int, delta string) {
	content, ok := message.Content[index].(llm.TextContent)
	if ok {
		content.Text += delta
		message.Content[index] = content
	}
}

// updateToolArguments 用最新 JSON 参数替换工具调用参数
func updateToolArguments(message *llm.AssistantMessage, index int, raw string) {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return
	}
	call, ok := message.Content[index].(llm.ToolCall)
	if ok {
		call.Arguments = args
		message.Content[index] = call
	}
}

func apiError(prefix, code, message string) error {
	if code != "" {
		return fmt.Errorf("%s: %s: %s", prefix, code, message)
	}
	return fmt.Errorf("%s: %s", prefix, message)
}
