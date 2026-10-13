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
	stream := cl.api.Responses.NewStreaming(ctx, params)
	events := make(chan llm.AssistantMessageEvent)
	go func() {
		defer close(events)
		defer stream.Close()
		// abortPartial 返回终止时携带已收到内容的 partial；对齐 Pi 保留 abort 时的部分输出
		partial := &llm.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID}
		abortPartial := func() *llm.AssistantMessage {
			snapshot := assistantMessageSnapshot(partial)
			snapshot.StopReason = llm.StopReasonAborted
			return snapshot
		}
		// 终止事件用阻塞式发送保证必达；普通事件保留 select 以便取消时及时退出
		abort := func(cause error) {
			sendTerminal(ctx, events, llm.AssistantMessageErrorEvent{
				Reason:  classifyTerminalReason(ctx, cause),
				Err:     cause,
				Partial: abortPartial(),
			})
		}
		if !sendEvent(ctx, events, llm.AssistantMessageStartEvent{Partial: assistantMessageSnapshot(partial)}) {
			// Start 事件被取消拦截时同样要送达终止事件；否则消费端只见通道关闭、无任何失败信息
			abort(fmt.Errorf("openai: stream setup canceled: %w", context.Canceled))
			return
		}
		slots := map[outputSlotKey]outputSlot{}
		terminal := false
		send := func(event llm.AssistantMessageEvent) bool {
			if eventPartial(event) == nil {
				event = withEventPartial(event, assistantMessageSnapshot(partial))
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
			case "response.content_part.added":
				if !addContentPartSlot(partial, slots, ev.OutputIndex, ev.ContentIndex, ev.Part.Type, send) {
					return
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				slot, ok := slots[outputSlotKey{outputIndex: ev.OutputIndex, contentIndex: ev.ContentIndex}]
				if !ok {
					slot, ok = slots[outputSlotKey{outputIndex: ev.OutputIndex}]
				}
				if !ok || slot.kind != llm.EventThinkingStart {
					continue
				}
				appendThinking(partial, slot.contentIdx, ev.Delta)
				if !send(llm.AssistantMessageThinkingDeltaEvent{ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
					return
				}
			case "response.reasoning_summary_part.done":
				slot, ok := slots[outputSlotKey{outputIndex: ev.OutputIndex}]
				if !ok || slot.kind != llm.EventThinkingStart {
					continue
				}
				appendThinking(partial, slot.contentIdx, "\n\n")
				if !send(llm.AssistantMessageThinkingDeltaEvent{ContentIndex: slot.contentIdx, Delta: "\n\n"}) {
					return
				}
			case "response.output_text.delta", "response.refusal.delta":
				slot, ok := slots[outputSlotKey{outputIndex: ev.OutputIndex, contentIndex: ev.ContentIndex}]
				if !ok || slot.kind != llm.EventTextStart {
					continue
				}
				appendText(partial, slot.contentIdx, ev.Delta)
				if !send(llm.AssistantMessageTextDeltaEvent{ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
					return
				}
			case "response.function_call_arguments.delta":
				slot, ok := slots[outputSlotKey{outputIndex: ev.OutputIndex}]
				if !ok || slot.kind != llm.EventToolCallStart {
					continue
				}
				slot.arguments += ev.Delta
				slots[outputSlotKey{outputIndex: ev.OutputIndex}] = slot
				updateToolArguments(partial, slot.contentIdx, slot.arguments)
				if !send(llm.AssistantMessageToolCallDeltaEvent{ContentIndex: slot.contentIdx, Delta: ev.Delta}) {
					return
				}
			case "response.function_call_arguments.done":
				slot, ok := slots[outputSlotKey{outputIndex: ev.OutputIndex}]
				if !ok || slot.kind != llm.EventToolCallStart {
					continue
				}
				previous := slot.arguments
				slot.arguments = ev.Arguments
				slots[outputSlotKey{outputIndex: ev.OutputIndex}] = slot
				updateToolArguments(partial, slot.contentIdx, slot.arguments)
				if strings.HasPrefix(ev.Arguments, previous) {
					delta := ev.Arguments[len(previous):]
					if delta != "" && !send(llm.AssistantMessageToolCallDeltaEvent{ContentIndex: slot.contentIdx, Delta: delta}) {
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
					send(llm.AssistantMessageErrorEvent{Err: err})
					return
				}
				if err := incompleteResponseError(ev.Response); err != nil {
					send(llm.AssistantMessageErrorEvent{Err: err})
					return
				}
				if !send(llm.AssistantMessageDoneEvent{Message: msg}) {
					return
				}
				return
			case "response.failed":
				terminal = true
				errResp := ev.Response.Error
				send(llm.AssistantMessageErrorEvent{Err: classifyAPIError("openai: request failed", string(errResp.Code), errResp.Message)})
				return
			case "error":
				terminal = true
				send(llm.AssistantMessageErrorEvent{Err: classifyAPIError("openai", ev.Code, ev.Message)})
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

// sendTerminal 向通道投递终止事件；消费端始终在 range 通道，阻塞发送保证必达，
// 消除取消竞态（对齐 Pi 的 EventStream.push 无条件入队语义）
func sendTerminal(ctx context.Context, events chan<- llm.AssistantMessageEvent, event llm.AssistantMessageEvent) {
	events <- event
}

// classifyTerminalReason 判断终止原因：取消/超时属于 aborted，其余按真实错误；
// 与 Pi 的 options?.signal?.aborted ? "aborted" : "error" 语义一致
func classifyTerminalReason(ctx context.Context, err error) llm.StopReason {
	if ctx != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return llm.StopReasonAborted
	}
	return llm.StopReasonError
}

// sendEvent 在上下文未取消时向模型事件通道发送事件
func sendEvent(ctx context.Context, events chan<- llm.AssistantMessageEvent, event llm.AssistantMessageEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func eventPartial(event llm.AssistantMessageEvent) *llm.AssistantMessage {
	switch event := event.(type) {
	case llm.AssistantMessageStartEvent:
		return event.Partial
	case llm.AssistantMessageTextStartEvent:
		return event.Partial
	case llm.AssistantMessageTextDeltaEvent:
		return event.Partial
	case llm.AssistantMessageTextEndEvent:
		return event.Partial
	case llm.AssistantMessageThinkingStartEvent:
		return event.Partial
	case llm.AssistantMessageThinkingDeltaEvent:
		return event.Partial
	case llm.AssistantMessageThinkingEndEvent:
		return event.Partial
	case llm.AssistantMessageToolCallStartEvent:
		return event.Partial
	case llm.AssistantMessageToolCallDeltaEvent:
		return event.Partial
	case llm.AssistantMessageToolCallEndEvent:
		return event.Partial
	case llm.AssistantMessageDoneEvent:
		return event.Partial
	case llm.AssistantMessageErrorEvent:
		return event.Partial
	}
	return nil
}

func withEventPartial(event llm.AssistantMessageEvent, partial *llm.AssistantMessage) llm.AssistantMessageEvent {
	switch event := event.(type) {
	case llm.AssistantMessageStartEvent:
		event.Partial = partial
		return event
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
	case llm.AssistantMessageDoneEvent:
		event.Partial = partial
		return event
	case llm.AssistantMessageErrorEvent:
		event.Partial = partial
		return event
	default:
		return event
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
		return errors.New("openai: response incomplete")
	}
	return fmt.Errorf("openai: response incomplete: %s", response.IncompleteDetails.Reason)
}

// addOutputSlot 为新增 output item 创建可持续更新的 assistant 内容块
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

// finishOutputSlot 用 output_item.done 的完整 item 固化内容并发出结束事件
func finishOutputSlot(partial *llm.AssistantMessage, slots map[outputSlotKey]outputSlot, outputIndex int64, item responses.ResponseOutputItemUnion, send func(llm.AssistantMessageEvent) bool) bool {
	slot, ok := slots[outputSlotKey{outputIndex: outputIndex}]
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

// finishMessageSlots 按 message 的 content index 分别固化文本和拒答内容
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

// classifyAPIError 按错误码与消息给 API 错误分类，上下文溢出包上 ErrContextOverflow 供上层做压缩恢复
func classifyAPIError(prefix, code, message string) error {
	err := apiError(prefix, code, message)
	if isContextOverflowSignature(code, message) {
		// 双重 %w 保留溢出标记与原始错误链，便于上游按错误码 errors.As 提取细节
		return fmt.Errorf("%w: %w", llm.ErrContextOverflow, err)
	}
	return err
}

// classifyStreamError 给流中断错误分类，检测原始 API 错误文本中的溢出签名
func classifyStreamError(prefix string, err error) error {
	if isContextOverflowSignature("", err.Error()) {
		return fmt.Errorf("%w: %s: %w", llm.ErrContextOverflow, prefix, err)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// isContextOverflowSignature 判断错误码或消息是否为上下文溢出类签名。
// 错误文本可能回显请求内容，因此只保留足够具体的签名，宽泛短语（如 "context window"）不参与匹配
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
