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
		effort := resolveReasoningEffort(model, opts)
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(effort),
			Summary: shared.ReasoningSummaryAuto,
		}
		params.Include = append(params.Include, responses.ResponseIncludableReasoningEncryptedContent)
	}
	if len(model.SamplingParams) > 0 {
		extra := make(map[string]any, len(model.SamplingParams))
		for key, value := range model.SamplingParams {
			// 保留键不得覆盖 BuildParams 自行构建的核心请求字段
			if !reservedParamKeys[key] {
				extra[key] = value
			}
		}
		if len(extra) > 0 {
			params.SetExtraFields(extra)
		}
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

// reservedParamKeys 是 BuildParams 自行构建的核心请求字段，
// models.json 的自由采样参数不得与它们同名覆盖
var reservedParamKeys = map[string]bool{
	"model": true, "input": true, "stream": true, "max_output_tokens": true,
	"tools": true, "tool_choice": true, "reasoning": true, "include": true,
}

// thinkingLevels 是 pi 的扩展思考等级体系，顺序从低到高
var thinkingLevels = []llm.ThinkingLevel{
	llm.ThinkingLevelOff, llm.ThinkingLevelMinimal, llm.ThinkingLevelLow, llm.ThinkingLevelMedium,
	llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh, llm.ThinkingLevelMax,
}

// resolveReasoningEffort 按 pi 语义把思考等级解析为 reasoning effort：
// 1.无显式等级：沿用模型默认 effort（缺省 medium），保持既有行为；
//
//	不采用 pi 无等级时经 map.off 回落到 "none" 的语义，避免改变历史默认
//
// 2.有显式等级：先经 clampThinkingLevel 收敛到模型支持的最近可用等级
// 3.收敛后为 off：发 effort "none" 或 map.off 的映射值（pi 流程中 off 经 void 后落入无等级分支）
// 4.其余等级：命中映射用映射值，未命中原样透传
// 非 reasoning 模型不经此函数（外层 if model.Reasoning 拦截）
func resolveReasoningEffort(model llm.Model, opts llm.Options) llm.ReasoningEffort {
	if opts.ThinkingLevel == "" {
		effort := model.ReasoningEffort
		if effort == "" {
			effort = llm.ReasoningEffortMedium
		}
		return effort
	}
	level := clampThinkingLevel(model, opts.ThinkingLevel)
	if level == llm.ThinkingLevelOff {
		if model.ThinkingLevelMap != nil {
			if mapped, ok := model.ThinkingLevelMap[llm.ThinkingLevelOff]; ok && mapped != nil {
				return *mapped
			}
		}
		return llm.ReasoningEffort("none")
	}
	if mapped, ok := model.ThinkingLevelMap[level]; ok && mapped != nil {
		return *mapped
	}
	return llm.ReasoningEffort(level)
}

// clampThinkingLevel 把请求的思考等级收敛到模型支持的最近可用等级（对齐 pi clampThinkingLevel）
func clampThinkingLevel(model llm.Model, level llm.ThinkingLevel) llm.ThinkingLevel {
	available := supportedThinkingLevels(model)
	for _, l := range available {
		if l == level {
			return level
		}
	}
	requestedIndex := -1
	for i, l := range thinkingLevels {
		if l == level {
			requestedIndex = i
			break
		}
	}
	// 未知等级直接落到首个可用等级
	if requestedIndex == -1 {
		return firstThinkingLevel(available)
	}
	// 先向更高等级收敛，再向更低
	for i := requestedIndex; i < len(thinkingLevels); i++ {
		if containsThinkingLevel(available, thinkingLevels[i]) {
			return thinkingLevels[i]
		}
	}
	for i := requestedIndex - 1; i >= 0; i-- {
		if containsThinkingLevel(available, thinkingLevels[i]) {
			return thinkingLevels[i]
		}
	}
	return firstThinkingLevel(available)
}

// supportedThinkingLevels 返回模型支持的思考等级：非 reasoning 模型只有 off；
// reasoning 模型默认全档可用，但显式 null 禁用该等级，xhigh/max 须显式映射才可用
func supportedThinkingLevels(model llm.Model) []llm.ThinkingLevel {
	if !model.Reasoning {
		return []llm.ThinkingLevel{llm.ThinkingLevelOff}
	}
	var levels []llm.ThinkingLevel
	for _, level := range thinkingLevels {
		mapped, ok := model.ThinkingLevelMap[level]
		if ok && mapped == nil {
			continue
		}
		if (level == llm.ThinkingLevelXHigh || level == llm.ThinkingLevelMax) && !ok {
			continue
		}
		levels = append(levels, level)
	}
	return levels
}

func containsThinkingLevel(levels []llm.ThinkingLevel, level llm.ThinkingLevel) bool {
	for _, l := range levels {
		if l == level {
			return true
		}
	}
	return false
}

func firstThinkingLevel(levels []llm.ThinkingLevel) llm.ThinkingLevel {
	if len(levels) > 0 {
		return levels[0]
	}
	return llm.ThinkingLevelOff
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
