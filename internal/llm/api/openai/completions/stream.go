package completions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type Client struct{ api sdk.Client }

type toolState struct {
	contentIndex int
	call         llm.ToolCall
	// arguments 用 Builder 累加，避免逐 delta 的字符串复制
	arguments strings.Builder
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
		if !llm.SendEvent(ctx, events, llm.AssistantMessageStartEvent{Partial: llm.CloneMessage(partial)}) {
			cause := ctx.Err()
			if cause == nil {
				cause = context.Canceled
			}
			llm.SendTerminal(events, llm.AssistantMessageErrorEvent{Reason: llm.StopReasonAborted, Err: cause, Partial: llm.CloneMessage(partial)})
			return
		}
		st := &streamState{partial: partial, textIndex: -1, thinkingIndex: -1, tools: make(map[int64]*toolState)}
		send := func(event llm.AssistantMessageEvent) bool {
			return llm.SendEvent(ctx, events, llm.WithPartial(event, llm.CloneMessage(partial)))
		}
		for stream.Next() {
			if !st.handleChunk(ctx, stream.Current(), model, send) {
				return
			}
		}
		if err := stream.Err(); err != nil {
			llm.SendTerminal(events, llm.AssistantMessageErrorEvent{Reason: llm.StopReasonForError(ctx, nil), Err: err, Partial: llm.CloneMessage(partial)})
			return
		}
		for _, state := range st.tools {
			args, err := decodeArguments(state.arguments.String())
			if err != nil {
				llm.SendTerminal(events, llm.AssistantMessageErrorEvent{Reason: llm.StopReasonError, Err: err, Partial: llm.CloneMessage(partial)})
				return
			}
			state.call.Arguments = args
			partial.Content[state.contentIndex] = state.call
			if !send(llm.AssistantMessageToolCallEndEvent{ContentIndex: state.contentIndex, ToolCall: state.call}) {
				return
			}
		}
		if st.textIndex >= 0 {
			if !send(llm.AssistantMessageTextEndEvent{ContentIndex: st.textIndex, Content: llm.TextAt(partial, st.textIndex)}) {
				return
			}
		}
		if st.thinkingIndex >= 0 {
			if !send(llm.AssistantMessageThinkingEndEvent{ContentIndex: st.thinkingIndex, Content: llm.ThinkingAt(partial, st.thinkingIndex)}) {
				return
			}
		}
		if ctx.Err() != nil {
			llm.SendTerminal(events, llm.AssistantMessageErrorEvent{Reason: llm.StopReasonAborted, Err: ctx.Err(), Partial: llm.CloneMessage(partial)})
			return
		}
		if !st.terminal {
			partial.StopReason = llm.StopReasonStop
		}
		if len(st.tools) > 0 && !st.terminal {
			partial.StopReason = llm.StopReasonToolUse
		}
		if partial.StopReason == llm.StopReasonError {
			llm.SendTerminal(events, llm.AssistantMessageErrorEvent{Reason: llm.StopReasonError, Err: errors.New("openai: provider rejected the completion"), Partial: llm.CloneMessage(partial)})
			return
		}
		llm.SendTerminal(events, llm.AssistantMessageDoneEvent{Message: llm.CloneMessage(partial), Partial: llm.CloneMessage(partial)})
	}()
	return events, nil
}

// streamState 是一次流式响应里的可变累加状态
type streamState struct {
	partial       *llm.AssistantMessage
	textIndex     int
	thinkingIndex int
	tools         map[int64]*toolState
	// terminal 表示收到过 finish_reason
	terminal bool
}

// handleChunk 处理一个 SSE chunk；返回 false 表示事件投递失败需终止
func (st *streamState) handleChunk(ctx context.Context, chunk sdk.ChatCompletionChunk, model llm.Model, send func(llm.AssistantMessageEvent) bool) bool {
	raw := map[string]any{}
	_ = json.Unmarshal([]byte(chunk.RawJSON()), &raw)
	st.partial.ResponseID = chunk.ID
	if chunk.Model != "" && chunk.Model != model.ID {
		st.partial.Model = chunk.Model
	}
	if usage, ok := usageFromRaw(raw); ok {
		st.partial.Usage = usage
	}
	choice, ok := firstChoice(chunk)
	if !ok {
		return true
	}
	if choice.FinishReason != "" {
		st.partial.StopReason = mapStopReason(choice.FinishReason)
		st.terminal = true
	}
	if !st.appendText(choice, send) {
		return false
	}
	if !st.appendThinking(raw, send) {
		return false
	}
	return st.appendToolCalls(choice, send)
}

// appendText 追加正文增量，首次出现时先补 text_start
func (st *streamState) appendText(choice sdk.ChatCompletionChunkChoice, send func(llm.AssistantMessageEvent) bool) bool {
	delta := choice.Delta.Content
	if delta == "" {
		delta = choice.Delta.Refusal
	}
	if delta == "" {
		return true
	}
	if st.textIndex < 0 {
		st.textIndex = len(st.partial.Content)
		st.partial.Content = append(st.partial.Content, llm.TextContent{})
		if !send(llm.AssistantMessageTextStartEvent{ContentIndex: st.textIndex}) {
			return false
		}
	}
	llm.AppendText(st.partial, st.textIndex, delta)
	return send(llm.AssistantMessageTextDeltaEvent{ContentIndex: st.textIndex, Delta: delta})
}

// appendThinking 追加思考增量，兼容三种字段名
func (st *streamState) appendThinking(raw map[string]any, send func(llm.AssistantMessageEvent) bool) bool {
	for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		delta := rawDeltaString(raw, field)
		if delta == "" {
			continue
		}
		if st.thinkingIndex < 0 {
			st.thinkingIndex = len(st.partial.Content)
			st.partial.Content = append(st.partial.Content, llm.ThinkingContent{ThinkingSignature: field})
			if !send(llm.AssistantMessageThinkingStartEvent{ContentIndex: st.thinkingIndex}) {
				return false
			}
		}
		llm.AppendThinking(st.partial, st.thinkingIndex, delta)
		return send(llm.AssistantMessageThinkingDeltaEvent{ContentIndex: st.thinkingIndex, Delta: delta})
	}
	return true
}

// appendToolCalls 按 index 累加工具调用增量
func (st *streamState) appendToolCalls(choice sdk.ChatCompletionChunkChoice, send func(llm.AssistantMessageEvent) bool) bool {
	for _, delta := range choice.Delta.ToolCalls {
		state := st.tools[delta.Index]
		if state == nil {
			state = &toolState{contentIndex: len(st.partial.Content)}
			state.call = llm.ToolCall{ID: delta.ID, Arguments: map[string]any{}}
			st.partial.Content = append(st.partial.Content, state.call)
			st.tools[delta.Index] = state
			if !send(llm.AssistantMessageToolCallStartEvent{ContentIndex: state.contentIndex, ToolCall: state.call}) {
				return false
			}
		}
		if delta.ID != "" {
			state.call.ID = delta.ID
		}
		if delta.Function.Name != "" {
			state.call.Name = delta.Function.Name
		}
		state.arguments.WriteString(delta.Function.Arguments)
		st.partial.Content[state.contentIndex] = state.call
		if delta.Function.Arguments != "" {
			if !send(llm.AssistantMessageToolCallDeltaEvent{ContentIndex: state.contentIndex, Delta: delta.Function.Arguments}) {
				return false
			}
		}
	}
	return true
}

func firstChoice(chunk sdk.ChatCompletionChunk) (sdk.ChatCompletionChunkChoice, bool) {
	if len(chunk.Choices) == 0 {
		return sdk.ChatCompletionChunkChoice{}, false
	}
	return chunk.Choices[0], true
}

func usageFromRaw(raw map[string]any) (llm.Usage, bool) {
	usageRaw, ok := raw["usage"].(map[string]any)
	if !ok {
		// usage 可能挂在首个 choice 上，字段缺失即视为没有 usage
		choice, ok := firstRawChoice(raw)
		if !ok {
			return llm.Usage{}, false
		}
		usageRaw, ok = choice["usage"].(map[string]any)
		if !ok {
			return llm.Usage{}, false
		}
	}
	prompt := rawInt(usageRaw, "prompt_tokens")
	completion := rawInt(usageRaw, "completion_tokens")
	total := rawInt(usageRaw, "total_tokens")
	cacheRead := rawInt(usageRaw, "cached_tokens")
	if details, ok := usageRaw["prompt_tokens_details"].(map[string]any); ok {
		cacheRead = max(cacheRead, rawInt(details, "cached_tokens"))
	}
	if cacheRead == 0 {
		cacheRead = rawInt(usageRaw, "prompt_cache_hit_tokens")
	}
	cacheWrite := rawInt(usageRaw, "cache_write_tokens")
	details, hasDetails := usageRaw["prompt_tokens_details"].(map[string]any)
	if hasDetails {
		cacheWrite = max(cacheWrite, rawInt(details, "cache_write_tokens"))
	}
	reasoning := 0
	if outputDetails, ok := usageRaw["completion_tokens_details"].(map[string]any); ok {
		reasoning = rawInt(outputDetails, "reasoning_tokens")
	}
	cacheWrite1h := rawInt(usageRaw, "cache_write_1h_tokens")
	if hasDetails {
		cacheWrite1h = max(cacheWrite1h, rawInt(details, "cache_write_1h_tokens"))
	}
	usage := llm.NormalizeUsage(prompt, completion, total, cacheRead, cacheWrite, cacheWrite1h, reasoning)
	return usage, hasUsageValues(usage)
}

func hasUsageValues(usage llm.Usage) bool {
	return usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.CacheReadTokens != 0 ||
		usage.CacheWriteTokens != 0 || usage.CacheWrite1HTokens != 0 || usage.ReasoningTokens != 0 || usage.TotalTokens != 0
}

func rawInt(values map[string]any, key string) int {
	value, ok := values[key]
	if !ok {
		return 0
	}
	switch value := value.(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		return 0
	}
}

// rawDeltaString 尽力从原始 JSON 取增量字段，缺失即返回空串
func rawDeltaString(raw map[string]any, field string) string {
	choice, ok := firstRawChoice(raw)
	if !ok {
		return ""
	}
	delta, ok := choice["delta"].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := delta[field].(string)
	return value
}

// firstRawChoice 取原始 JSON 的第一个 choice，缺失或类型不符时返回 false
func firstRawChoice(raw map[string]any) (map[string]any, bool) {
	choices, ok := raw["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, false
	}
	choice, ok := choices[0].(map[string]any)
	return choice, ok
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

// decodeArguments 解析工具参数；坏 JSON 必须上报，否则错误会被归因到工具层
func decodeArguments(raw string) (map[string]any, error) {
	args := map[string]any{}
	if raw == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("openai: tool arguments are not valid JSON: %w", err)
	}
	return args, nil
}
