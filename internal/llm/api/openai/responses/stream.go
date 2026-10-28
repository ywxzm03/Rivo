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
	kind       string
	contentIdx int
	arguments  string
}

type outputSlotKey struct {
	outputIndex  int64
	contentIndex int64
}

func NewClient(apiKey, baseURL string) *Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Client{api: sdk.NewClient(opts...)}
}

// Stream 将 Responses SSE 事件转换为带 content index 的统一模型事件
func (cl *Client) Stream(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
	if model.API != llm.APIOpenAIResponses {
		return nil, fmt.Errorf("openai: protocol %q not supported yet", model.API)
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
	stream := cl.api.Responses.NewStreaming(ctx, params, requestOptions...)
	events := make(chan llm.AssistantMessageEvent)
	go func() {
		defer close(events)
		defer stream.Close()
		// abortPartial 返回终止时携带已收到内容的 partial
		partial := &llm.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID}
		abortPartial := func(reason llm.StopReason) *llm.AssistantMessage {
			snapshot := llm.CloneMessage(partial)
			snapshot.StopReason = reason
			return snapshot
		}
		// 终止事件用阻塞式发送保证必达，普通事件保留 select 以便取消时及时退出
		abort := func(cause error) {
			reason := llm.StopReasonForError(ctx, cause)
			llm.SendTerminal(events, llm.AssistantMessageErrorEvent{
				Reason:  reason,
				Err:     cause,
				Partial: abortPartial(reason),
			})
		}
		if !llm.SendEvent(ctx, events, llm.AssistantMessageStartEvent{Partial: llm.CloneMessage(partial)}) {
			// Start 事件被取消拦截时同样要送达终止事件，否则消费端只见通道关闭
			cause := ctx.Err()
			if cause == nil {
				cause = context.Canceled
			}
			abort(fmt.Errorf("openai: stream setup canceled: %w", cause))
			return
		}
		slots := map[outputSlotKey]outputSlot{}
		terminal := false
		send := func(event llm.AssistantMessageEvent) bool {
			if llm.EventPartial(event) == nil {
				event = llm.WithPartial(event, llm.CloneMessage(partial))
			}
			return llm.SendEvent(ctx, events, event)
		}
		for stream.Next() {
			ev := stream.Current()
			if !handleResponseEvent(ev, partial, slots, send, &terminal) {
				return
			}
		}
		if !terminal {
			if err := stream.Err(); err != nil {
				abort(classifyStreamError("openai: stream interrupted", err))
			} else {
				abort(errors.New("openai: stream ended before a terminal event"))
			}
		}
	}()
	return events, nil
}

// handleResponseEvent 处理一个 SSE 事件。
// done 为真表示流已终止、调用方应直接返回
func handleResponseEvent(ev responses.ResponseStreamEventUnion, partial *llm.AssistantMessage, slots map[outputSlotKey]outputSlot, send func(llm.AssistantMessageEvent) bool, terminal *bool) bool {
	switch ev.Type {
	case "response.created":
		partial.ResponseID = ev.Response.ID
	case "response.output_item.added":
		if !addOutputSlot(partial, slots, ev.OutputIndex, ev.Item, send) {
			return false
		}
	case "response.content_part.added":
		if !addContentPartSlot(partial, slots, ev.OutputIndex, ev.ContentIndex, ev.Part.Type, send) {
			return false
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		slot, found := slots[outputSlotKey{outputIndex: ev.OutputIndex, contentIndex: ev.ContentIndex}]
		if !found {
			slot, found = slots[outputSlotKey{outputIndex: ev.OutputIndex}]
		}
		if !found || slot.kind != llm.EventThinkingStart {
			return true
		}
		llm.AppendThinking(partial, slot.contentIdx, ev.Delta)
		if !send(llm.AssistantMessageThinkingDeltaEvent{ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
			return false
		}
	case "response.reasoning_summary_part.done":
		slot, found := slots[outputSlotKey{outputIndex: ev.OutputIndex}]
		if !found || slot.kind != llm.EventThinkingStart {
			return true
		}
		llm.AppendThinking(partial, slot.contentIdx, "\n\n")
		if !send(llm.AssistantMessageThinkingDeltaEvent{ContentIndex: slot.contentIdx, Delta: "\n\n"}) {
			return false
		}
	case "response.output_text.delta", "response.refusal.delta":
		slot, found := slots[outputSlotKey{outputIndex: ev.OutputIndex, contentIndex: ev.ContentIndex}]
		if !found || slot.kind != llm.EventTextStart {
			return true
		}
		llm.AppendText(partial, slot.contentIdx, ev.Delta)
		if !send(llm.AssistantMessageTextDeltaEvent{ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
			return false
		}
	case "response.function_call_arguments.delta":
		slot, found := slots[outputSlotKey{outputIndex: ev.OutputIndex}]
		if !found || slot.kind != llm.EventToolCallStart {
			return true
		}
		slot.arguments += ev.Delta
		slots[outputSlotKey{outputIndex: ev.OutputIndex}] = slot
		updateToolArguments(partial, slot.contentIdx, slot.arguments)
		if !send(llm.AssistantMessageToolCallDeltaEvent{ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
			return false
		}
	case "response.function_call_arguments.done":
		slot, found := slots[outputSlotKey{outputIndex: ev.OutputIndex}]
		if !found || slot.kind != llm.EventToolCallStart {
			return true
		}
		previous := slot.arguments
		slot.arguments = ev.Arguments
		slots[outputSlotKey{outputIndex: ev.OutputIndex}] = slot
		updateToolArguments(partial, slot.contentIdx, slot.arguments)
		if strings.HasPrefix(ev.Arguments, previous) {
			delta := ev.Arguments[len(previous):]
			if delta != "" && !send(llm.AssistantMessageToolCallDeltaEvent{ContentIndex: slot.contentIdx, Delta: delta}) {
				return false
			}
		}
	case "response.output_item.done":
		if !finishOutputSlot(partial, slots, ev.OutputIndex, ev.Item, send) {
			return false
		}
	case "response.completed", "response.incomplete":
		*terminal = true
		msg, _, err := BuildAssistantMessage(ev.Response)
		if err != nil {
			send(llm.AssistantMessageErrorEvent{Err: err})
			return false
		}
		if err := incompleteResponseError(ev.Response); err != nil {
			send(llm.AssistantMessageErrorEvent{Err: err})
			return false
		}
		if !send(llm.AssistantMessageDoneEvent{Message: msg}) {
			return false
		}
		return false
	case "response.failed":
		*terminal = true
		errResp := ev.Response.Error
		send(llm.AssistantMessageErrorEvent{Err: classifyAPIError("openai: request failed", string(errResp.Code), errResp.Message)})
		return false
	case "error":
		*terminal = true
		send(llm.AssistantMessageErrorEvent{Err: classifyAPIError("openai", ev.Code, ev.Message)})
		return false
	}
	return true
}

func incompleteResponseError(response responses.Response) error {
	if response.Status != responses.ResponseStatusIncomplete || response.IncompleteDetails.Reason == "max_output_tokens" {
		return nil
	}
	if response.IncompleteDetails.Reason == "" {
		return errors.New("openai: response incomplete")
	}
	return fmt.Errorf("openai: response incomplete: %s", response.IncompleteDetails.Reason)
}

func addOutputSlot(partial *llm.AssistantMessage, slots map[outputSlotKey]outputSlot, outputIndex int64, item responses.ResponseOutputItemUnion, send func(llm.AssistantMessageEvent) bool) bool {
	idx := len(partial.Content)
	switch item.Type {
	case "reasoning":
		partial.Content = append(partial.Content, llm.ThinkingContent{})
		slots[outputSlotKey{outputIndex: outputIndex}] = outputSlot{kind: llm.EventThinkingStart, contentIdx: idx}
		return send(llm.AssistantMessageThinkingStartEvent{ContentIndex: idx})
	case "message":
		partial.Content = append(partial.Content, llm.TextContent{})
		slots[outputSlotKey{outputIndex: outputIndex}] = outputSlot{kind: llm.EventTextStart, contentIdx: idx}
		return send(llm.AssistantMessageTextStartEvent{ContentIndex: idx})
	case "function_call":
		call := llm.ToolCall{ID: item.CallID, Name: item.Name, Arguments: map[string]any{}}
		partial.Content = append(partial.Content, call)
		slots[outputSlotKey{outputIndex: outputIndex}] = outputSlot{kind: llm.EventToolCallStart, contentIdx: idx, arguments: item.Arguments.OfString}
		if item.Arguments.OfString != "" {
			updateToolArguments(partial, idx, item.Arguments.OfString)
			call = partial.Content[idx].(llm.ToolCall)
		}
		return send(llm.AssistantMessageToolCallStartEvent{ContentIndex: idx, ToolCall: call})
	default:
		send(llm.AssistantMessageErrorEvent{Err: fmt.Errorf("openai: unsupported response output item type %q", item.Type)})
		return false
	}
}

// addContentPartSlot 为 message output item 的独立内容块创建 slot
func addContentPartSlot(partial *llm.AssistantMessage, slots map[outputSlotKey]outputSlot, outputIndex, contentIndex int64, partType string, send func(llm.AssistantMessageEvent) bool) bool {
	key := outputSlotKey{outputIndex: outputIndex, contentIndex: contentIndex}
	if _, ok := slots[key]; ok {
		return true
	}
	idx := len(partial.Content)
	var kind string
	var content llm.Content
	switch partType {
	case "output_text", "refusal":
		kind, content = llm.EventTextStart, llm.TextContent{}
	case "reasoning_text":
		kind, content = llm.EventThinkingStart, llm.ThinkingContent{}
	default:
		send(llm.AssistantMessageErrorEvent{Err: fmt.Errorf("openai: unsupported response content part type %q", partType)})
		return false
	}
	partial.Content = append(partial.Content, content)
	slots[key] = outputSlot{kind: kind, contentIdx: idx}
	if kind == llm.EventThinkingStart {
		return send(llm.AssistantMessageThinkingStartEvent{ContentIndex: idx})
	}
	return send(llm.AssistantMessageTextStartEvent{ContentIndex: idx})
}

// finishOutputSlot 用 output_item.done 的完整 item 固化内容
func finishOutputSlot(partial *llm.AssistantMessage, slots map[outputSlotKey]outputSlot, outputIndex int64, item responses.ResponseOutputItemUnion, send func(llm.AssistantMessageEvent) bool) bool {
	slot, ok := slots[outputSlotKey{outputIndex: outputIndex}]
	if !ok {
		return true
	}
	switch item.Type {
	case "reasoning":
		thinking, signature, err := reasoningThinking(item)
		if err != nil {
			return send(llm.AssistantMessageErrorEvent{Reason: llm.StopReasonError, Err: err})
		}
		partial.Content[slot.contentIdx] = llm.ThinkingContent{Thinking: thinking, ThinkingSignature: signature}
		delete(slots, outputSlotKey{outputIndex: outputIndex})
		return send(llm.AssistantMessageThinkingEndEvent{ContentIndex: slot.contentIdx, Content: thinking})
	case "message":
		return finishMessageSlots(partial, slots, outputIndex, item.Content, send)
	case "function_call":
		call, err := toolCallFromItem(item)
		if err != nil {
			send(llm.AssistantMessageErrorEvent{Err: err})
			return false
		}
		partial.Content[slot.contentIdx] = call
		delete(slots, outputSlotKey{outputIndex: outputIndex})
		return send(llm.AssistantMessageToolCallEndEvent{ContentIndex: slot.contentIdx, ToolCall: call})
	default:
		send(llm.AssistantMessageErrorEvent{Err: fmt.Errorf("openai: unsupported response output item type %q", item.Type)})
		return false
	}
}

func finishMessageSlots(partial *llm.AssistantMessage, slots map[outputSlotKey]outputSlot, outputIndex int64, contents []responses.ResponseOutputMessageContentUnion, send func(llm.AssistantMessageEvent) bool) bool {
	if len(contents) == 0 {
		slot, ok := slots[outputSlotKey{outputIndex: outputIndex}]
		delete(slots, outputSlotKey{outputIndex: outputIndex})
		if !ok {
			return send(llm.AssistantMessageTextEndEvent{ContentIndex: 0})
		}
		return send(llm.AssistantMessageTextEndEvent{ContentIndex: slot.contentIdx})
	}
	for contentIndex, part := range contents {
		key := outputSlotKey{outputIndex: outputIndex, contentIndex: int64(contentIndex)}
		slot, ok := slots[key]
		if !ok {
			if !addContentPartSlot(partial, slots, outputIndex, int64(contentIndex), part.Type, send) {
				return false
			}
			slot = slots[key]
		}
		text := part.Text
		if part.Type == "refusal" {
			text = part.Refusal
		}
		partial.Content[slot.contentIdx] = llm.TextContent{Text: text}
		delete(slots, key)
		if !send(llm.AssistantMessageTextEndEvent{ContentIndex: slot.contentIdx, Content: text}) {
			return false
		}
	}
	return true
}

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

// classifyAPIError 把上下文溢出包上 ErrContextOverflow 供上层做压缩恢复
func classifyAPIError(prefix, code, message string) error {
	err := apiError(prefix, code, message)
	if isContextOverflowSignature(code, message) {
		// 双重 %w 同时保留溢出标记与原始错误链
		return fmt.Errorf("%w: %w", llm.ErrContextOverflow, err)
	}
	return err
}

func classifyStreamError(prefix string, err error) error {
	if isContextOverflowSignature("", err.Error()) {
		return fmt.Errorf("%w: %s: %w", llm.ErrContextOverflow, prefix, err)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// isContextOverflowSignature 只匹配足够具体的溢出签名；错误文本可能回显请求内容，
// 宽泛短语（如 "context window"）不参与匹配
func isContextOverflowSignature(code, message string) bool {
	text := strings.ToLower(code + " " + message)
	for _, signature := range []string{
		"context_length_exceeded",
		"above_model_max_context_tokens",
		"maximum context length",
		"too many input tokens",
	} {
		if strings.Contains(text, signature) {
			return true
		}
	}
	return false
}
