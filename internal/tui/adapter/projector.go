package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func encodeToolArguments(arguments map[string]any) string {
	if arguments == nil {
		return "{}"
	}
	data, err := json.MarshalIndent(arguments, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", arguments)
	}
	return string(data)
}

func formatToolResult(result llm.ToolResult) string {
	var parts []string
	for _, content := range result.Content {
		switch content := content.(type) {
		case llm.TextContent:
			parts = append(parts, content.Text)
		case llm.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]", content.MIMEType, len(content.Data)))
		}
	}
	if result.Details != nil {
		data, err := json.MarshalIndent(result.Details, "", "  ")
		if err != nil {
			parts = append(parts, fmt.Sprintf("%v", result.Details))
		} else {
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}

func assistantContent(message llm.AssistantMessage) (string, []tui.ThinkingBlockView, []llm.ToolCall) {
	var text string
	var blocks []tui.ThinkingBlockView
	var calls []llm.ToolCall
	for index, content := range message.Content {
		switch content := content.(type) {
		case llm.TextContent:
			text += content.Text
		case llm.ThinkingContent:
			blocks = append(blocks, tui.ThinkingBlockView{Index: index, Text: content.Thinking, Tokens: max(0, len(content.Thinking)/4), TokensExact: false})
		case llm.ToolCall:
			calls = append(calls, content)
		}
	}
	return text, blocks, calls
}

func toolCallView(call llm.ToolCall) tui.ToolCallView {
	return tui.ToolCallView{ID: call.ID, Name: call.Name, Arguments: encodeToolArguments(call.Arguments), Status: tui.ToolPending}
}

func (r *Runtime) groupAnchorLocked() (int, *tui.ToolGroupView) {
	if r.activeToolAnchorID == "" {
		return -1, nil
	}
	if index := r.messageIndexLocked(r.activeToolAnchorID); index >= 0 {
		return index, r.messages[index].ToolGroup
	}

	return -1, nil
}

func (r *Runtime) newToolGroupLocked(anchorID string) *tui.ToolGroupView {
	return &tui.ToolGroupView{ID: anchorID + "/tools", Active: true}
}

func cloneToolGroup(group *tui.ToolGroupView) *tui.ToolGroupView {
	if group == nil {
		return nil
	}
	copy := *group
	copy.Calls = append([]tui.ToolCallView(nil), group.Calls...)
	copy.Thinking = append([]tui.ThinkingBlockView(nil), group.Thinking...)
	return &copy
}

func (r *Runtime) ensureToolGroupLocked(anchorID string) *tui.ToolGroupView {
	if index, current := r.groupAnchorLocked(); index >= 0 && current != nil {
		return cloneToolGroup(current)
	}
	group := r.newToolGroupLocked(anchorID)
	r.activeToolAnchorID = anchorID
	for _, message := range r.messages {
		if message.ID == anchorID {
			return group
		}
	}
	r.upsertLocked(tui.Message{ID: anchorID, Role: "assistant", ToolGroup: cloneToolGroup(group)})
	return group
}

func (r *Runtime) setToolGroupLocked(group *tui.ToolGroupView) {
	index, _ := r.groupAnchorLocked()
	if index >= 0 {
		r.messages[index].ToolGroup = cloneToolGroup(group)
		r.messages[index].Revision++
	}
}

func findToolCall(group *tui.ToolGroupView, id string) int {
	for i := range group.Calls {
		if group.Calls[i].ID == id {
			return i
		}
	}
	return -1
}

func (r *Runtime) updateThinkingLocked(group *tui.ToolGroupView, blocks []tui.ThinkingBlockView, active bool) {
	group.Thinking = mergeThinking(group.Thinking, blocks, liveThinking)
	for i := range group.Thinking {
		group.Thinking[i].Active = active
	}
}

func (r *Runtime) sealToolGroupLocked() {
	index, current := r.groupAnchorLocked()
	if index < 0 || current == nil {
		return
	}
	group := cloneToolGroup(current)
	for i := range group.Calls {
		if group.Calls[i].Status == tui.ToolPending {
			group.Calls[i].Status = tui.ToolInterrupted
		}
	}
	for i := range group.Thinking {
		group.Thinking[i].Active = false
	}
	group.Active, group.Sealed = false, true
	r.messages[index].ToolGroup = group
	r.messages[index].Revision++
	r.activeToolAnchorID = ""
}

func (r *Runtime) toolStartLocked(event llm.ToolExecutionStartEvent) tui.Message {
	if r.toolStarted == nil {
		r.toolStarted = make(map[string]time.Time)
	}
	started := time.Now()
	r.toolStarted[event.ToolCallID] = started
	group := r.ensureToolGroupLocked(r.currentAssistantID)
	if index := findToolCall(group, event.ToolCallID); index >= 0 {
		group.Calls[index].Name = event.ToolName
		group.Calls[index].Arguments = encodeToolArguments(event.Arguments)
		group.Calls[index].StartedAt = started
		group.Calls[index].Status = tui.ToolPending
	} else {
		group.Calls = append(group.Calls, tui.ToolCallView{ID: event.ToolCallID, Name: event.ToolName, Arguments: encodeToolArguments(event.Arguments), Status: tui.ToolPending, StartedAt: started})
	}
	r.setToolGroupLocked(group)
	return r.messages[r.groupAnchorIndexLocked()]
}

func (r *Runtime) toolUpdateLocked(event llm.ToolExecutionUpdateEvent) tui.Message {
	group := r.ensureToolGroupLocked(r.currentAssistantID)
	index := findToolCall(group, event.ToolCallID)
	if index < 0 {
		group.Calls = append(group.Calls, tui.ToolCallView{ID: event.ToolCallID, Name: event.ToolName, Arguments: encodeToolArguments(event.Arguments), Status: tui.ToolPending})
		index = len(group.Calls) - 1
	}
	if event.ToolName != "" {
		group.Calls[index].Name = event.ToolName
	}
	if event.Arguments != nil {
		group.Calls[index].Arguments = encodeToolArguments(event.Arguments)
	}
	group.Calls[index].Result = formatToolResult(event.PartialResult)
	r.setToolGroupLocked(group)
	return r.messages[r.groupAnchorIndexLocked()]
}

func (r *Runtime) toolEndLocked(event llm.ToolExecutionEndEvent) tui.Message {
	group := r.ensureToolGroupLocked(r.currentAssistantID)
	index := findToolCall(group, event.ToolCallID)
	if index < 0 {
		group.Calls = append(group.Calls, tui.ToolCallView{ID: event.ToolCallID, Name: event.ToolName, Status: tui.ToolPending})
		index = len(group.Calls) - 1
	}
	group.Calls[index].Name = event.ToolName
	group.Calls[index].Result = formatToolResult(event.Result)
	group.Calls[index].Status = tui.ToolSuccess
	if event.IsError || event.Result.IsError {
		group.Calls[index].Status = tui.ToolError
	}
	if start := r.toolStarted[event.ToolCallID]; !start.IsZero() {
		group.Calls[index].StartedAt = start
		group.Calls[index].Elapsed = time.Since(start)
		group.Calls[index].EndedAt = time.Now()
	}
	delete(r.toolStarted, event.ToolCallID)
	r.setToolGroupLocked(group)
	return r.messages[r.groupAnchorIndexLocked()]
}

func (r *Runtime) groupAnchorIndexLocked() int {
	index, _ := r.groupAnchorLocked()
	if index >= 0 {
		return index
	}
	return len(r.messages) - 1
}

func (r *Runtime) addAssistantCallsLocked(group *tui.ToolGroupView, calls []llm.ToolCall) {
	for _, call := range calls {
		if findToolCall(group, call.ID) >= 0 {
			continue
		}
		group.Calls = append(group.Calls, toolCallView(call))
	}
}

func (r *Runtime) liveAssistantLocked(message llm.AssistantMessage, id string, stream llm.AssistantMessageEvent) (tui.Message, bool) {
	text, blocks, calls := assistantContent(message)
	group := (*tui.ToolGroupView)(nil)
	if index, current := r.groupAnchorLocked(); index >= 0 && current != nil {
		group = cloneToolGroup(current)
	}
	if len(calls) > 0 {
		if group == nil {
			group = r.ensureToolGroupLocked(id)
		}
		r.addAssistantCallsLocked(group, calls)
		r.setToolGroupLocked(group)
	}
	if len(blocks) > 0 && group == nil {
		// A provider may deliver the completed assistant snapshot without a
		// separate thinking update; preserve that reasoning block for the transcript.
		group = r.ensureToolGroupLocked(id)
	}
	if stream == nil && text == "" && len(blocks) == 0 && len(calls) == 0 && group == nil {
		// Keep a visible working block while the provider has opened an assistant
		// turn but has not emitted its first thinking, text, or tool event yet.
		group = r.ensureToolGroupLocked(id)
	}
	if stream == nil && len(blocks) > 0 {
		r.updateThinkingLocked(group, blocks, false)
		r.setToolGroupLocked(group)
	}
	if stream != nil {
		switch current := stream.(type) {
		case llm.AssistantMessageThinkingStartEvent, llm.AssistantMessageThinkingDeltaEvent, llm.AssistantMessageThinkingEndEvent:
			if group == nil {
				group = r.ensureToolGroupLocked(id)
			}
			active := stream.Type() != llm.EventThinkingEnd
			r.updateThinkingLocked(group, blocks, active)
			r.setToolGroupLocked(group)
		case llm.AssistantMessageTextStartEvent, llm.AssistantMessageTextDeltaEvent, llm.AssistantMessageTextEndEvent:
			if strings.TrimSpace(text) != "" && group != nil {
				r.setToolGroupLocked(group)
				r.sealToolGroupLocked()
			}
			_ = current
		}
	}
	converted := tui.Message{ID: id, Role: "assistant", Text: text}
	if group != nil {
		if index, current := r.groupAnchorLocked(); index >= 0 && current != nil {
			if r.messages[index].ID == id {
				converted.ToolGroup = cloneToolGroup(current)
			} else if strings.TrimSpace(text) == "" {
				// 连续 assistant turn 属于同一个紧凑工具组，thinking 流更新继续写入原锚点
				// 避免为下一轮模型输出创建不可见的重复消息
				converted = r.messages[index]
				converted.ToolGroup = cloneToolGroup(current)
			}
		}
	}
	if converted.ToolGroup == nil {
		if index := r.messageIndexLocked(id); index >= 0 {
			converted.ToolGroup = cloneToolGroup(r.messages[index].ToolGroup)
		}
	}
	return converted, converted.Text != "" || converted.ToolGroup != nil || converted.Thinking != ""
}

func (r *Runtime) messageIndexLocked(id string) int {
	if r.messageIndex == nil {
		r.messageIndex = make(map[string]int, len(r.messages))
		r.lastAssistantIndex = -1
		r.hasToolGroup = false
		for i, message := range r.messages {
			r.messageIndex[message.ID] = i
			if message.Role == "assistant" && message.Text != "" {
				r.lastAssistantIndex = i
			}
			if message.ToolGroup != nil {
				r.hasToolGroup = true
			}
		}
	}
	if index, ok := r.messageIndex[id]; ok {
		return index
	}
	return -1
}

func (r *Runtime) addDividerLocked() int {
	r.messageIndexLocked("")
	if r.turnStarted.IsZero() || !r.hasToolGroup || r.lastAssistantIndex < 0 {
		return -1
	}
	elapsed := time.Since(r.turnStarted)
	if elapsed < time.Second {
		return -1
	}
	index := r.lastAssistantIndex
	r.messages[index].Divider = &tui.TurnDividerView{Elapsed: elapsed}
	r.messages[index].Revision++
	return index
}

func (r *Runtime) projectContext(view compaction.ContextView) []tui.Message {
	projected := make([]tui.Message, 0, len(view.Entries))
	var group *tui.ToolGroupView
	groupIndex := -1
	seal := func() {
		if groupIndex >= 0 && group != nil {
			copy := cloneToolGroup(group)
			copy.Active, copy.Sealed = false, true
			for i := range copy.Calls {
				if copy.Calls[i].Status == tui.ToolPending {
					copy.Calls[i].Status = tui.ToolInterrupted
				}
			}
			for i := range copy.Thinking {
				copy.Thinking[i].Active = false
			}
			projected[groupIndex].ToolGroup = copy
		}
		group, groupIndex = nil, -1
	}
	for _, entry := range view.Entries {
		// 压缩快照与摘要只供模型使用，不进入显示投影
		if entry.Kind == compaction.ContextCompaction {
			continue
		}
		for _, message := range entry.Messages {
			id := r.sourceID(entry.SourceID, "message")
			switch current := message.(type) {
			case llm.UserMessage:
				seal()
				if text := textContent(current.Content); text != "" {
					projected = append(projected, tui.Message{ID: id, Role: "user", Text: text})
				}
			case llm.AssistantMessage:
				if current.StopReason == llm.StopReasonError {
					continue
				}
				aborted := current.StopReason == llm.StopReasonAborted
				text, thinking, calls := assistantContent(current)
				if len(calls) == 0 {
					seal()
					if text != "" || len(thinking) > 0 || aborted {
						converted := tui.Message{ID: id, Role: "assistant", Text: text, Aborted: aborted}
						if len(thinking) > 0 {
							converted.ToolGroup = &tui.ToolGroupView{ID: id + "/tools", Thinking: thinking, Sealed: true}
						}
						projected = append(projected, converted)
					}
					continue
				}
				if group == nil {
					group = &tui.ToolGroupView{ID: id + "/tools"}
					groupIndex = len(projected)
					projected = append(projected, tui.Message{ID: id, Role: "assistant", Text: text, Aborted: aborted, ToolGroup: cloneToolGroup(group)})
				} else {
					projected[groupIndex].Text += text
					if aborted {
						projected[groupIndex].Aborted = true
					}
				}
				group.Thinking = mergeThinking(group.Thinking, thinking, replayThinking)
				for _, call := range calls {
					if findToolCall(group, call.ID) < 0 {
						group.Calls = append(group.Calls, toolCallView(call))
					}
				}
				projected[groupIndex].ToolGroup = cloneToolGroup(group)
			case llm.ToolResultMessage:
				if group == nil {
					group = &tui.ToolGroupView{ID: id + "/tools"}
					groupIndex = len(projected)
					projected = append(projected, tui.Message{ID: id, Role: "assistant", ToolGroup: cloneToolGroup(group)})
				}
				toolIndex := findToolCall(group, current.ToolCallID)
				if toolIndex < 0 {
					group.Calls = append(group.Calls, tui.ToolCallView{ID: current.ToolCallID, Name: current.ToolName, Status: tui.ToolPending})
					toolIndex = len(group.Calls) - 1
				}
				group.Calls[toolIndex].Name = current.ToolName
				group.Calls[toolIndex].Result = formatToolResult(llm.ToolResult{Content: current.Content, Details: current.Details, IsError: current.IsError})
				group.Calls[toolIndex].Status = tui.ToolSuccess
				if current.IsError {
					group.Calls[toolIndex].Status = tui.ToolError
				}
				projected[groupIndex].ToolGroup = cloneToolGroup(group)
			}
		}
	}
	seal()
	return projected
}

// thinkingPolicy 显式保留实时更新与历史重放的显示差异
type thinkingPolicy bool

const (
	liveThinking   thinkingPolicy = false
	replayThinking thinkingPolicy = true
)

func mergeThinking(previous, next []tui.ThinkingBlockView, policy thinkingPolicy) []tui.ThinkingBlockView {
	if policy == replayThinking {
		return append(previous, next...)
	}
	return append([]tui.ThinkingBlockView(nil), next...)
}
