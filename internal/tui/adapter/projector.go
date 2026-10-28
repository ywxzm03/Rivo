package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func encodeToolArguments(arguments map[string]any) string {
	if arguments == nil {
		return "{}"
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return ""
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
func encodeToolDetails(details llm.ToolDetails) string {
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
	var text strings.Builder
	var blocks []tui.ThinkingBlockView
	var calls []llm.ToolCall
	for index, content := range message.Content {
		switch content := content.(type) {
		case llm.TextContent:
			text.WriteString(content.Text)
		case llm.ThinkingContent:
			blocks = append(blocks, tui.ThinkingBlockView{Index: index, Text: content.Thinking})
		case llm.ToolCall:
			calls = append(calls, content)
		}
	}
	return text.String(), blocks, calls
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
	return &tui.ToolGroupView{ID: anchorID + "/tools"}
}

func (r *Runtime) ensureToolGroupLocked(anchorID string) *tui.ToolGroupView {
	if index, current := r.groupAnchorLocked(); index >= 0 && current != nil {
		return current
	}
	group := r.newToolGroupLocked(anchorID)
	r.activeToolAnchorID = anchorID
	for _, message := range r.messages {
		if message.ID == anchorID {
			return group
		}
	}
	r.upsertLocked(tui.Message{ID: anchorID, Role: "assistant", ToolGroup: group.Clone()})
	return group
}

func (r *Runtime) setToolGroupLocked(group *tui.ToolGroupView) {
	index, _ := r.groupAnchorLocked()
	if index >= 0 {
		r.messages[index].ToolGroup = group
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
	}
}

func (r *Runtime) sealToolGroupLocked() {
	index, current := r.groupAnchorLocked()
	if index < 0 || current == nil {
		return
	}
	for i := range current.Calls {
		if current.Calls[i].Status == tui.ToolPending {
			current.Calls[i].Status = tui.ToolInterrupted
		}
	}
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
		group = current
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
		switch stream.(type) {
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
		}
	}
	converted := tui.Message{ID: id, Role: "assistant", Text: text}
	if group != nil {
		if index, current := r.groupAnchorLocked(); index >= 0 && current != nil {
			if r.messages[index].ID == id {
				converted.ToolGroup = current.Clone()
			} else if strings.TrimSpace(text) == "" {
				// 连续 assistant turn 属于同一个紧凑工具组，thinking 流继续写入原锚点
				converted = r.messages[index]
				converted.ToolGroup = current.Clone()
			}
		}
	}
	if converted.ToolGroup == nil {
		if index := r.messageIndexLocked(id); index >= 0 {
			converted.ToolGroup = r.messages[index].ToolGroup.Clone()
		}
	}
	return converted, converted.Text != "" || converted.ToolGroup != nil
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

func (r *Runtime) projectContext(view ctxview.ContextView) []tui.Message {
	projected := make([]tui.Message, 0, len(view.Entries))
	var group *tui.ToolGroupView
	groupIndex := -1
	seal := func() {
		if groupIndex >= 0 && group != nil {
			copy := group.Clone()
			for i := range copy.Calls {
				if copy.Calls[i].Status == tui.ToolPending {
					copy.Calls[i].Status = tui.ToolInterrupted
				}
			}
			projected[groupIndex].ToolGroup = copy
		}
		group, groupIndex = nil, -1
	}
	round := 0
	for _, entry := range view.Entries {
		// 压缩快照与摘要只供模型使用
		if entry.Kind == ctxview.ContextCompaction {
			continue
		}
		for _, message := range entry.Messages {
			id := r.sourceID(entry.SourceID, "message")
			switch current := message.(type) {
			case llm.UserMessage:
				// 先封组再判隐藏:通知落在两个工具轮之间时,工具组仍要正常收尾
				seal()
				if current.Hidden {
					continue
				}
				text := llm.TextOf(current.Content)
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
							converted.ToolGroup = &tui.ToolGroupView{ID: id + "/tools", Thinking: thinking}
						}
						projected = append(projected, converted)
					}
					continue
				}
				if group == nil {
					group = &tui.ToolGroupView{ID: id + "/tools"}
					groupIndex = len(projected)
					projected = append(projected, tui.Message{ID: id, Role: "assistant", Text: text, Aborted: aborted, ToolGroup: group.Clone()})
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
				projected[groupIndex].ToolGroup = group.Clone()
			case llm.ToolResultMessage:
				if group == nil {
					group = &tui.ToolGroupView{ID: id + "/tools"}
					groupIndex = len(projected)
					projected = append(projected, tui.Message{ID: id, Role: "assistant", ToolGroup: group.Clone()})
				}
				toolIndex := findToolCall(group, current.ToolCallID)
				if toolIndex < 0 {
					group.Calls = append(group.Calls, tui.ToolCallView{ID: current.ToolCallID, Name: current.ToolName, Round: round, Status: tui.ToolPending, ArgsComplete: true})
					toolIndex = len(group.Calls) - 1
				}
				group.Calls[toolIndex].Name = current.ToolName
				group.Calls[toolIndex].Result = formatToolResult(llm.ToolResult{Content: current.Content, IsError: current.IsError})
				group.Calls[toolIndex].Details = encodeToolDetails(current.Details)
				group.Calls[toolIndex].Status = tui.ToolSuccess
				if current.IsError {
					group.Calls[toolIndex].Status = tui.ToolError
				}
				projected[groupIndex].ToolGroup = group.Clone()
			}
		}
	}
	seal()
	return projected
}

// eventResult 是单个事件投影出的待发布结果
type eventResult struct {
	// notifications 直接透传给 TUI 的消息
	notifications []tea.Msg
	// message 需要 upsert 的消息
	message *tui.Message
	// previousID 非空表示该消息换了 ID
	previousID string
	// status 非空表示要更新状态栏
	status string
	// queueChanged 表示队列镜像需要重发
	queueChanged bool
}

func (r *Runtime) handleEvent(event llm.AgentEvent) error {
	r.mu.Lock()
	result := eventResult{}
	previousToolAnchor := r.activeToolAnchorID
	r.applyEventLocked(event, &result)
	notifications := result.notifications
	if result.message != nil {
		r.upsertLocked(*result.message)
		if result.previousID != "" {
			notifications = append(notifications, tui.MessageCommittedMsg{PreviousID: result.previousID, Message: r.publishLocked(r.messageIndexLocked(result.message.ID))})
		} else {
			notifications = append(notifications, tui.MessageMsg(r.publishLocked(r.messageIndexLocked(result.message.ID))))
		}
	}
	if previousToolAnchor != "" && r.activeToolAnchorID == "" {
		if index := r.messageIndexLocked(previousToolAnchor); index >= 0 {
			notifications = append(notifications, tui.MessageMsg(r.publishLocked(index)))
		}
	}
	if result.status != "" {
		notifications = append(notifications, tui.StatusMsg(result.status))
	}
	if result.queueChanged && r.app != nil {
		notifications = append(notifications, tui.QueueStateMsg{
			Steering: r.app.QueueMessages(agent.QueueSteering), SteeringInputs: payloadsFromMessages(queueUserMessages(r.app, agent.QueueSteering)), SteeringRevision: r.app.QueueRevision(agent.QueueSteering),
			FollowUp: r.app.QueueMessages(agent.QueueFollowUp), FollowUpInputs: payloadsFromMessages(queueUserMessages(r.app, agent.QueueFollowUp)), FollowUpRevision: r.app.QueueRevision(agent.QueueFollowUp),
		})
		notifications = append(notifications, tui.SteeringStateMsg{Messages: r.app.QueueMessages(agent.QueueSteering), Inputs: payloadsFromMessages(queueUserMessages(r.app, agent.QueueSteering)), Revision: r.app.QueueRevision(agent.QueueSteering)})
	}
	send := r.send
	r.mu.Unlock()
	// 通知可能阻塞，发送时不持有投影或取消所需的锁
	if send != nil {
		for _, message := range notifications {
			send(message)
		}
	}
	return nil
}

// applyEventLocked 按事件种类更新投影，结果写入 result
func (r *Runtime) applyEventLocked(event llm.AgentEvent, result *eventResult) {
	switch event := event.(type) {
	case llm.AgentStartEvent:
		r.applyAgentStartLocked(event, result)
	case llm.TurnStartEvent:
		if r.turnStarted.IsZero() {
			r.turnStarted = time.Now()
		}
	case llm.MessageStartEvent:
		r.applyMessageStartLocked(event, result)
	case llm.MessageUpdateEvent:
		if assistant, ok := event.Message.(llm.AssistantMessage); ok {
			converted, ok := r.liveAssistantLocked(assistant, r.currentAssistantID, event.AssistantMessageEvent)
			if ok {
				result.message = &converted
			}
		}
	case llm.MessageEndEvent:
		if projected, renamed := r.projectMessageEnd(event); projected != nil {
			result.message, result.previousID = projected, renamed
		}
	case llm.AgentEndEvent:
		r.applyAgentEndLocked(event, result)
	case llm.ToolExecutionStartEvent:
		converted := r.toolStartLocked(event)
		result.message = &converted
	case llm.ToolExecutionUpdateEvent:
		converted := r.toolUpdateLocked(event)
		result.message = &converted
	case llm.ToolExecutionEndEvent:
		converted := r.toolEndLocked(event)
		result.message = &converted
	case llm.WarningEvent:
		r.warningSeq++
		result.message = &tui.Message{
			ID:   fmt.Sprintf("%s/warning/%d", r.namespace, r.warningSeq),
			Role: "notice",
			Text: "Warning: " + event.Message,
		}
	case llm.CompactionStartEvent:
		result.status = "Compacting context…"
	case llm.CompactionEndEvent:
		r.applyCompactionEndLocked(event, result)
	}
}

// applyAgentStartLocked 记录 turn 起点并补发后台 run 通知
func (r *Runtime) applyAgentStartLocked(event llm.AgentStartEvent, result *eventResult) {
	if r.turnStarted.IsZero() || !r.turnRetrying {
		r.turnStarted = time.Now()
	}
	r.turnRetrying = false
	// 用户请求驱动的 run 已由 TUI 自己置忙碌态;这里只补后台 run
	if !r.externalRunSeen && !r.operationActive {
		r.externalRunSeen = true
		result.notifications = append(result.notifications, tui.BackgroundRunMsg{Running: true})
	}
}

// applyMessageStartLocked 为用户或 assistant 消息开一个新投影
func (r *Runtime) applyMessageStartLocked(event llm.MessageStartEvent, result *eventResult) {
	switch current := event.Message.(type) {
	case llm.UserMessage:
		r.sealToolGroupLocked()
		r.userMessages++
		if r.userMessages > 1 {
			result.queueChanged = true
			r.userID = fmt.Sprintf("%s/live/%d/user/%d", r.namespace, r.sequence, r.userMessages)
		}
		converted, ok := r.messageFromLLMLocked(event.Message, r.userID)
		if ok {
			result.message = &converted
		}
	case llm.AssistantMessage:
		r.assistantTurn++
		r.currentAssistantID = fmt.Sprintf("%s/live/%d/assistant/%d", r.namespace, r.sequence, r.assistantTurn)
		converted, ok := r.liveAssistantLocked(current, r.currentAssistantID, nil)
		if ok {
			result.message = &converted
		}
	}
}

// applyAgentEndLocked 收尾工具组，重试时保留分组
func (r *Runtime) applyAgentEndLocked(event llm.AgentEndEvent, result *eventResult) {
	if !event.WillRetry && r.externalRunSeen {
		r.externalRunSeen = false
		result.notifications = append(result.notifications, tui.BackgroundRunMsg{Running: false})
	}
	r.sealToolGroupLocked()
	if event.WillRetry {
		r.turnRetrying = true
		return
	}
	if index := r.addDividerLocked(); index >= 0 {
		result.notifications = append(result.notifications, tui.MessageMsg(r.publishLocked(index)))
	}
	r.turnStarted = time.Time{}
	r.turnRetrying = false
}

// applyCompactionEndLocked 更新压缩结果提示
func (r *Runtime) applyCompactionEndLocked(event llm.CompactionEndEvent, result *eventResult) {
	if event.Error != "" {
		result.status = "Compaction failed"
		r.compactionNotice = nil
		return
	}
	result.status = "Working"
	r.resetNeeded = true
	r.compactionNoticeSeq++
	notice := tui.Message{
		ID:   fmt.Sprintf("%s/compaction/%d", r.namespace, r.compactionNoticeSeq),
		Role: "summary",
		Text: fmt.Sprintf("Context compacted (%s): %d -> %d tokens", event.Reason, event.TokensBefore, event.TokensAfter),
	}
	r.compactionNotice = &notice
}

func (r *Runtime) upsertLocked(message tui.Message) {
	index := r.messageIndexLocked(message.ID)
	if message.ToolGroup != nil {
		r.hasToolGroup = true
	}
	if message.Role == "assistant" && message.Text != "" {
		if index < 0 {
			r.lastAssistantIndex = len(r.messages)
		} else {
			r.lastAssistantIndex = index
		}
	}
	if index >= 0 {
		message.Revision = r.messages[index].Revision + 1
		r.messages[index] = message
		return
	}
	if message.Revision == 0 {
		message.Revision = 1
	}
	r.messageIndex[message.ID] = len(r.messages)
	r.messages = append(r.messages, message)
}

// publishLocked 复制要发给 TUI 的消息，保证渲染层持有快照，
// 内部后续对 ToolGroup 的原地修改不会污染已发布的状态
func (r *Runtime) publishLocked(index int) tui.Message {
	if index < 0 || index >= len(r.messages) {
		return tui.Message{}
	}
	message := r.messages[index]
	if message.ToolGroup != nil {
		message.ToolGroup = message.ToolGroup.Clone()
	}
	return message
}

func (r *Runtime) sourceID(source, part string) string {
	return r.namespace + "/entry/" + source + "/" + part
}

func (r *Runtime) renameMessageLocked(oldID, newID string) {
	if index := r.messageIndexLocked(oldID); index >= 0 {
		delete(r.messageIndex, oldID)
		r.messageIndex[newID] = index
		r.messages[index].ID = newID
	}
	if r.userID == oldID {
		r.userID = newID
	}
	if r.currentAssistantID == oldID {
		r.currentAssistantID = newID
	}
	if r.activeToolAnchorID == oldID {
		r.activeToolAnchorID = newID
	}
}

func (r *Runtime) replaceMessagesLocked(view ctxview.ContextView) {
	messages := preserveDisplayMetadata(r.projectContext(view), r.messages)
	r.messages = messages
	r.messageIndex = nil
	r.activeToolAnchorID, r.currentAssistantID = "", ""
}

func (r *Runtime) resetProjectionLocked() {
	r.lastAssistantIndex = 0
	r.hasToolGroup = false
	r.operationLastAssistant, r.operationHadToolGroup = 0, false
	r.operationStart = 0
	r.operationEntries = nil
	r.userID = ""
	r.assistantTurn = 0
	r.currentAssistantID, r.activeToolAnchorID = "", ""
	r.toolStarted = nil
	r.turnStarted, r.turnRetrying = time.Time{}, false
	r.compactionNotice = nil
}

func (r *Runtime) partialAssistantLocked() *tui.Message {
	for i := len(r.messages) - 1; i >= 0; i-- {
		message := r.messages[i]
		if message.Role == "assistant" && message.ID == r.currentAssistantID && (message.Text != "" || message.ToolGroup != nil || message.Aborted) {
			copy := message
			return &copy
		}
		if message.Role == "user" {
			break
		}
	}
	return nil
}

func (r *Runtime) hasMessageLocked(message tui.Message) bool {
	return r.messageIndexLocked(message.ID) >= 0
}

// messageFromLLMLocked 把一条 llm 消息投影成 TUI 消息
// 隐藏消息(如子任务通知)只供模型阅读,返回 false 让调用方跳过渲染
func (r *Runtime) messageFromLLMLocked(message llm.Message, id string) (tui.Message, bool) {
	if llm.IsHiddenMessage(message) {
		return tui.Message{}, false
	}
	converted := tui.Message{ID: id}
	switch message := message.(type) {
	case llm.UserMessage:
		converted.Role, converted.Text = "user", llm.TextOf(message.Content)
		converted.Images = imageViews(message.Content)
		converted.Skill = skillInvocation(converted.Text)
	default:
		return tui.Message{}, false
	}
	return converted, converted.Text != "" || len(converted.Images) > 0
}
