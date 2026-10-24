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

// formatToolResult 只拼工具输出的文本内容；结构化详情单独走 encodeToolDetails，
// 否则 diff 之类的数据会以 JSON 形式混进结果正文
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
	return strings.Join(parts, "\n")
}

// encodeToolDetails 把工具的结构化详情编码成 JSON，供渲染层取 diff 等字段
func encodeToolDetails(details any) string {
	if details == nil {
		return ""
	}
	data, err := json.Marshal(details)
	if err != nil {
		return ""
	}
	return string(data)
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

func toolCallView(call llm.ToolCall, round int) tui.ToolCallView {
	return tui.ToolCallView{ID: call.ID, Name: call.Name, Round: round, Arguments: encodeToolArguments(call.Arguments), Status: tui.ToolPending}
}

// updateToolCallView 让已存在的调用跟随流式快照更新：名字、参数和“参数已写完”都随
// partial assistant 消息前进，渲染层才不会在整个参数流期间显示空标签
func updateToolCallView(view *tui.ToolCallView, call llm.ToolCall, round int, complete bool) {
	if call.Name != "" {
		view.Name = call.Name
	}
	if call.Arguments != nil {
		view.Arguments = encodeToolArguments(call.Arguments)
	}
	if round > 0 {
		view.Round = round
	}
	if complete {
		view.ArgsComplete = true
	}
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

// updateThinkingLocked 把本轮思考写进组里：同一轮覆盖，不同轮追加，
// 这样每轮的 thinking 都能留在它自己的调用前面
func (r *Runtime) updateThinkingLocked(group *tui.ToolGroupView, blocks []tui.ThinkingBlockView, round int, active bool) {
	for _, block := range blocks {
		block.Round = round
		block.Active = active
		replaced := false
		for i := range group.Thinking {
			if group.Thinking[i].Round == round && group.Thinking[i].Index == block.Index {
				group.Thinking[i] = block
				replaced = true
				break
			}
		}
		if !replaced {
			group.Thinking = append(group.Thinking, block)
		}
	}
	// 本轮缺席的旧块视为已结束，避免上一轮的思考一直转圈
	for i := range group.Thinking {
		if group.Thinking[i].Round != round {
			continue
		}
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
		group.Calls[index].ArgsComplete = true
	} else {
		group.Calls = append(group.Calls, tui.ToolCallView{ID: event.ToolCallID, Name: event.ToolName, Round: r.assistantTurn, Arguments: encodeToolArguments(event.Arguments), Status: tui.ToolPending, ArgsComplete: true, StartedAt: started})
	}
	r.setToolGroupLocked(group)
	return r.messages[r.groupAnchorIndexLocked()]
}

func (r *Runtime) toolUpdateLocked(event llm.ToolExecutionUpdateEvent) tui.Message {
	group := r.ensureToolGroupLocked(r.currentAssistantID)
	index := findToolCall(group, event.ToolCallID)
	if index < 0 {
		group.Calls = append(group.Calls, tui.ToolCallView{ID: event.ToolCallID, Name: event.ToolName, Round: r.assistantTurn, Arguments: encodeToolArguments(event.Arguments), Status: tui.ToolPending, ArgsComplete: true})
		index = len(group.Calls) - 1
	}
	if event.ToolName != "" {
		group.Calls[index].Name = event.ToolName
	}
	if event.Arguments != nil {
		group.Calls[index].Arguments = encodeToolArguments(event.Arguments)
	}
	group.Calls[index].Result = formatToolResult(event.PartialResult)
	group.Calls[index].Details = encodeToolDetails(event.PartialResult.Details)
	r.setToolGroupLocked(group)
	return r.messages[r.groupAnchorIndexLocked()]
}

func (r *Runtime) toolEndLocked(event llm.ToolExecutionEndEvent) tui.Message {
	group := r.ensureToolGroupLocked(r.currentAssistantID)
	index := findToolCall(group, event.ToolCallID)
	if index < 0 {
		group.Calls = append(group.Calls, tui.ToolCallView{ID: event.ToolCallID, Name: event.ToolName, Round: r.assistantTurn, Status: tui.ToolPending, ArgsComplete: true})
		index = len(group.Calls) - 1
	}
	group.Calls[index].Name = event.ToolName
	group.Calls[index].ArgsComplete = true
	group.Calls[index].Result = formatToolResult(event.Result)
	group.Calls[index].Details = encodeToolDetails(event.Result.Details)
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

// addAssistantCallsLocked 按 id upsert 调用：流式快照里名字与参数会持续变化，
// 已存在的调用必须跟着更新，否则会一直显示宣告时的空名字
func (r *Runtime) addAssistantCallsLocked(group *tui.ToolGroupView, calls []llm.ToolCall, round int, complete bool) {
	for _, call := range calls {
		if index := findToolCall(group, call.ID); index >= 0 {
			updateToolCallView(&group.Calls[index], call, round, complete)
			continue
		}
		view := toolCallView(call, round)
		view.ArgsComplete = complete
		group.Calls = append(group.Calls, view)
	}
}

func (r *Runtime) liveAssistantLocked(message llm.AssistantMessage, id string, stream llm.AssistantMessageEvent) (tui.Message, bool) {
	text, blocks, calls := assistantContent(message)
	group := (*tui.ToolGroupView)(nil)
	if index, current := r.groupAnchorLocked(); index >= 0 && current != nil {
		group = cloneToolGroup(current)
	}
	round := r.assistantTurn
	// 参数是否写完：toolcall_end 或后续执行事件都会把它标成 true
	complete := stream == nil || stream.Type() == llm.EventToolCallEnd
	if len(calls) > 0 {
		if group == nil {
			group = r.ensureToolGroupLocked(id)
		}
		r.addAssistantCallsLocked(group, calls, round, complete)
		r.setToolGroupLocked(group)
	}
	if len(blocks) > 0 && group == nil {
		// provider 可能只给完成后的 assistant 快照，没有独立的 thinking 更新
		group = r.ensureToolGroupLocked(id)
	}
	if stream == nil && text == "" && len(blocks) == 0 && len(calls) == 0 && group == nil {
		// provider 已开启 assistant turn 但还没有任何可见事件，保留一个工作块
		group = r.ensureToolGroupLocked(id)
	}
	if stream == nil && len(blocks) > 0 {
		r.updateThinkingLocked(group, blocks, round, false)
		r.setToolGroupLocked(group)
	}
	if stream != nil {
		switch current := stream.(type) {
		case llm.AssistantMessageThinkingStartEvent, llm.AssistantMessageThinkingDeltaEvent, llm.AssistantMessageThinkingEndEvent:
			if group == nil {
				group = r.ensureToolGroupLocked(id)
			}
			active := stream.Type() != llm.EventThinkingEnd
			r.updateThinkingLocked(group, blocks, round, active)
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
				// 连续 assistant turn 属于同一个紧凑工具组，thinking 流继续写入原锚点
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
	r.messages[index].Divider = &tui.TurnDividerView{Elapsed: elapsed, FinishedAt: time.Now()}
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
	round := 0
	for _, entry := range view.Entries {
		// 压缩快照与摘要只供模型使用
		if entry.Kind == compaction.ContextCompaction {
			continue
		}
		for _, message := range entry.Messages {
			id := r.sourceID(entry.SourceID, "message")
			switch current := message.(type) {
			case llm.UserMessage:
				seal()
				text := textContent(current.Content)
				images := imageViews(current.Content)
				if text != "" || len(images) > 0 {
					projected = append(projected, tui.Message{ID: id, Role: "user", Text: text, Images: images, Skill: skillInvocation(text)})
				}
			case llm.AssistantMessage:
				if current.StopReason == llm.StopReasonError {
					continue
				}
				aborted := current.StopReason == llm.StopReasonAborted
				text, thinking, calls := assistantContent(current)
				round++
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
				for _, block := range thinking {
					block.Round = round
					group.Thinking = append(group.Thinking, block)
				}
				for _, call := range calls {
					if index := findToolCall(group, call.ID); index >= 0 {
						updateToolCallView(&group.Calls[index], call, round, true)
						continue
					}
					view := toolCallView(call, round)
					view.ArgsComplete = true
					group.Calls = append(group.Calls, view)
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
					group.Calls = append(group.Calls, tui.ToolCallView{ID: current.ToolCallID, Name: current.ToolName, Round: round, Status: tui.ToolPending, ArgsComplete: true})
					toolIndex = len(group.Calls) - 1
				}
				group.Calls[toolIndex].Name = current.ToolName
				group.Calls[toolIndex].Result = formatToolResult(llm.ToolResult{Content: current.Content, Details: current.Details, IsError: current.IsError})
				group.Calls[toolIndex].Details = encodeToolDetails(current.Details)
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
