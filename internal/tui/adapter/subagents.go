package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func (r *Runtime) subagentSnapshot() tui.SubagentSnapshot {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil || activeApp.SubagentStore() == nil {
		return tui.SubagentSnapshot{}
	}
	return r.subagentSnapshotLocked()
}

func (r *Runtime) SubagentSnapshot() tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		activeApp := r.app
		r.mu.Unlock()
		if activeApp == nil || activeApp.SubagentStore() == nil {
			return tui.SubagentSnapshotMsg{Err: app.ErrSteeringUnavailable}
		}
		return tui.SubagentSnapshotMsg{Snapshot: r.subagentSnapshotLocked()}
	}
}

func (r *Runtime) SubagentRequest(request tui.SubagentRequest) tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		activeApp := r.app
		r.mu.Unlock()
		if activeApp == nil {
			return tui.SubagentSnapshotMsg{Err: app.ErrSteeringUnavailable, RestoreMessage: request.Message}
		}
		var err error
		switch request.Kind {
		case "enter":
			if !activeApp.EnterSubagentView(request.TaskID) {
				err = fmt.Errorf("subagent task %s not found", request.TaskID)
			}
		case "exit":
			activeApp.ExitSubagentView()
		case "queue":
			if request.Message == nil {
				err = fmt.Errorf("subagent: missing message")
			} else {
				err = activeApp.QueueSubagentMessage(request.TaskID, *request.Message)
			}
		case "continue":
			if request.Message == nil {
				err = fmt.Errorf("subagent: missing message")
			} else {
				_, err = activeApp.ContinueSubagent(context.Background(), request.TaskID, taskMessageText(*request.Message))
			}
		case "stop":
			err = activeApp.StopSubagent(request.TaskID)
		case "evict":
			if !activeApp.EvictSubagent(request.TaskID) {
				err = fmt.Errorf("subagent task %s cannot be evicted", request.TaskID)
			}
		}
		result := tui.SubagentSnapshotMsg{Snapshot: r.subagentSnapshot(), Err: err}
		if err != nil {
			result.RestoreMessage = request.Message
		}
		return result
	}
}

func taskMessageText(message llm.UserMessage) string {
	var parts []string
	for _, content := range message.Content {
		if text, ok := content.(llm.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// transcriptCache 是 TUI 侧的 transcript 增量投影缓存。
// 完整 transcript 常驻磁盘,内存只保留已投影的消息和读取偏移。
type transcriptCache struct {
	// stable 是已定型的前缀投影
	stable []tui.Message
	// pending 是尚未定型的尾部原文:assistant 的 tool call 还没拿到全部结果时,
	// 它的投影会缺 result,所以每次快照重新投影这段,等结果到齐再并入 stable
	pending []llm.Message
	offset  int64
	// start 是 stable 覆盖的源消息数,也是 pending 首条消息的绝对下标
	start int
}

// append 合并磁盘上的新消息,把已定型的部分并入 stable
func (t *transcriptCache) append(taskID string, messages []llm.Message) {
	if len(messages) == 0 {
		return
	}
	t.pending = append(t.pending, messages...)
	cut := resolvedPrefix(t.pending)
	if cut == 0 {
		return
	}
	t.stable = append(t.stable, projectSubagentMessages(taskID, t.start, t.pending[:cut])...)
	t.start += cut
	t.pending = append([]llm.Message(nil), t.pending[cut:]...)
}

// resolvedPrefix 返回尾部原文中可以定型投影的长度。
// 缺 tool result 的 assistant 只有在后续轮次已经开始时才能定型,
// 否则它的 tool call 可能马上拿到结果,必须留在 pending 里等下一帧
func resolvedPrefix(pending []llm.Message) int {
	results := make(map[string]bool)
	for _, message := range pending {
		if result, ok := message.(llm.ToolResultMessage); ok {
			results[result.ToolCallID] = true
		}
	}
	cut := 0
	for i, message := range pending {
		assistant, ok := message.(llm.AssistantMessage)
		if ok {
			missing := false
			for _, content := range assistant.Content {
				call, ok := content.(llm.ToolCall)
				if ok && !results[call.ID] {
					missing = true
					break
				}
			}
			// 后续已经出现新的 user 消息说明这一轮不会再补结果了
			if missing && !hasUserMessage(pending[i+1:]) {
				return i
			}
		}
		cut = i + 1
	}
	return cut
}

func hasUserMessage(messages []llm.Message) bool {
	for _, message := range messages {
		if _, ok := message.(llm.UserMessage); ok {
			return true
		}
	}
	return false
}

// seal 把残留尾部整体并入 stable:任务已终态,不会再有 tool result 补齐
func (t *transcriptCache) seal(taskID string) {
	if len(t.pending) == 0 {
		return
	}
	t.stable = append(t.stable, projectSubagentMessages(taskID, t.start, t.pending)...)
	t.start += len(t.pending)
	t.pending = nil
}

// subagentTranscript 返回指定任务的增量投影。首次调用从磁盘读全量,
// 之后只消费新写入的部分;尾部按当前内容重新投影,消息 ID 保持稳定
func (r *Runtime) subagentTranscript(store *task.Store, state *task.AgentState) []tui.Message {
	if r.subagentTranscripts == nil {
		r.subagentTranscripts = make(map[string]*transcriptCache)
	}
	cache := r.subagentTranscripts[state.ID]
	if cache == nil {
		cache = &transcriptCache{}
		r.subagentTranscripts[state.ID] = cache
	}
	// 按偏移读取:只返回上次快照之后新落盘的消息
	if messages, offset, err := store.ReadTaskMessages(state.ID, cache.offset); err == nil {
		cache.offset = offset
		cache.append(state.ID, messages)
	}
	if state.Base().Status.Terminal() {
		cache.seal(state.ID)
	}
	if len(cache.pending) == 0 {
		// 满容量切片:接收方 append 必须重新分配,不会写进缓存底层数组
		return cache.stable[:len(cache.stable):len(cache.stable)]
	}
	tail := projectSubagentMessages(state.ID, cache.start, cache.pending)
	projected := make([]tui.Message, 0, len(cache.stable)+len(tail))
	projected = append(projected, cache.stable...)
	return append(projected, tail...)
}

func (r *Runtime) projectSubagentTasks(store *task.Store, states []task.State, viewingTaskID string) []tui.SubagentTaskView {
	tasks := make([]tui.SubagentTaskView, 0, len(states))
	live := make(map[string]bool, len(states))
	for _, state := range states {
		if state == nil || state.Base() == nil {
			continue
		}
		base := state.Base()
		live[base.ID] = true
		view := tui.SubagentTaskView{
			ID: base.ID, Type: string(base.Type), Status: string(base.Status),
			Description: base.Description, OutputFile: base.OutputFile,
			StartTime: base.StartTime, EndTime: base.EndTime,
			LifecycleToken: base.LifecycleToken,
		}
		switch task := state.(type) {
		case *task.AgentState:
			view.Retain = task.Retain
			view.AgentType, view.Prompt, view.WorktreePath = task.AgentType, task.Prompt, task.WorktreePath
			view.PendingCount = len(task.PendingMessages)
			view.Revision = subagentTaskRevision(base, task.MessageCount, view.PendingCount)
			if task.ID == viewingTaskID {
				view.Messages = r.subagentTranscript(store, task)
			}
			if task.Result != nil {
				view.TotalTokens, view.ToolUses = task.Result.TotalTokens, task.Result.TotalToolUseCount
				view.Result = textBlocks(task.Result.Content)
			}
			if task.Progress != nil {
				view.TotalTokens = max(view.TotalTokens, task.Progress.TokenCount+task.Progress.LatestInputTokens)
				view.ToolUses = max(view.ToolUses, task.Progress.ToolUseCount)
				if task.Progress.LastActivity != nil {
					view.LastActivity = task.Progress.LastActivity.ActivityDescription
				}
			}
			view.Error = task.Error
		case *task.BashState:
			view.AgentType = "bash"
			view.Description = task.Command
			view.TotalTokens = 0
			view.Revision = subagentTaskRevision(base, 0, 0)
			if task.Result != nil && task.Result.Code != 0 {
				view.Error = fmt.Sprintf("exit code %d", task.Result.Code)
			}
		}
		tasks = append(tasks, view)
	}
	// 只保留仍存在的任务的投影缓存,避免驱逐后的 transcript 一直驻留内存
	for id := range r.subagentTranscripts {
		if !live[id] {
			delete(r.subagentTranscripts, id)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		// 按创建顺序固定,状态变化不重排;终止任务必须留在原位
		left, right := tasks[i], tasks[j]
		if !left.StartTime.Equal(right.StartTime) {
			return left.StartTime.Before(right.StartTime)
		}
		return left.ID < right.ID
	})
	return tasks
}

func subagentTaskRevision(base *task.StateBase, messageCount, pendingCount int) uint64 {
	revision := base.LifecycleToken + 1
	for _, value := range string(base.Status) {
		revision = revision*33 + uint64(value)
	}
	revision = revision*33 + uint64(messageCount)
	revision = revision*33 + uint64(pendingCount)
	return revision
}

func textBlocks(blocks []task.TextBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func projectSubagentMessages(taskID string, start int, messages []llm.Message) []tui.Message {
	results := make(map[string]llm.ToolResultMessage)
	for _, message := range messages {
		if result, ok := message.(llm.ToolResultMessage); ok {
			results[result.ToolCallID] = result
		}
	}
	projected := make([]tui.Message, 0, len(messages))
	for index, message := range messages {
		id := fmt.Sprintf("%s/entry/%d", taskID, start+index)
		switch current := message.(type) {
		case llm.UserMessage:
			text, images := taskMessageContent(current.Content)
			if text != "" || len(images) > 0 {
				projected = append(projected, tui.Message{ID: id, Role: "user", Text: text, Images: images})
			}
		case llm.AssistantMessage:
			var text, thinking string
			group := &tui.ToolGroupView{ID: id + "/tools", Sealed: true}
			for contentIndex, content := range current.Content {
				switch content := content.(type) {
				case llm.TextContent:
					text += content.Text
				case llm.ThinkingContent:
					thinking += content.Thinking
					group.Thinking = append(group.Thinking, tui.ThinkingBlockView{Index: contentIndex, Text: content.Thinking})
				case llm.ToolCall:
					// 还没有结果的调用必须显示为 pending,否则尾部投影会把
					// 正在执行的工具误标成成功
					call := tui.ToolCallView{ID: content.ID, Name: content.Name, Arguments: encodeTaskArguments(content.Arguments), Status: tui.ToolPending}
					if result, ok := results[content.ID]; ok {
						call.Result = formatTaskToolResult(result)
						call.Status = tui.ToolSuccess
						if result.IsError {
							call.Status = tui.ToolError
						}
					}
					group.Calls = append(group.Calls, call)
				}
			}
			if len(group.Calls) == 0 && len(group.Thinking) == 0 {
				group = nil
			}
			if text != "" || thinking != "" || group != nil {
				projected = append(projected, tui.Message{ID: id, Role: "assistant", Text: text, Thinking: thinking, ToolGroup: group})
			}
		}
	}
	return projected
}

func taskMessageContent(content []llm.Content) (string, []tui.ImageView) {
	var text string
	var images []tui.ImageView
	for _, item := range content {
		switch item := item.(type) {
		case llm.TextContent:
			if text != "" {
				text += "\n"
			}
			text += item.Text
		case llm.ImageContent:
			images = append(images, tui.ImageView{MIMEType: item.MIMEType})
		}
	}
	return text, images
}

func encodeTaskArguments(arguments map[string]any) string {
	data, err := json.MarshalIndent(arguments, "", "  ")
	if err != nil {
		return fmt.Sprint(arguments)
	}
	return string(data)
}

func formatTaskToolResult(result llm.ToolResultMessage) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(llm.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	if result.Details != nil {
		data, err := json.MarshalIndent(result.Details, "", "  ")
		if err == nil {
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}
