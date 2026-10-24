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
	"github.com/ywxzm03/Rivo/internal/subagent"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func (r *Runtime) subagentSnapshot() tui.SubagentSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	activeApp := r.app
	if activeApp == nil || activeApp.SubagentStore() == nil {
		return tui.SubagentSnapshot{}
	}
	r.subagentRevision++
	return tui.SubagentSnapshot{
		ViewingTaskID: activeApp.SubagentStore().ViewingTaskID(),
		Tasks:         projectSubagentTasks(activeApp.SubagentStore().Snapshot(), activeApp.SubagentStore().ViewingTaskID()),
		Revision:      r.subagentRevision,
	}
}

func (r *Runtime) SubagentSnapshot() tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		activeApp := r.app
		if activeApp == nil || activeApp.SubagentStore() == nil {
			r.mu.Unlock()
			return tui.SubagentSnapshotMsg{Err: app.ErrSteeringUnavailable}
		}
		store := activeApp.SubagentStore()
		viewingTaskID := store.ViewingTaskID()
		snapshot := tui.SubagentSnapshot{Tasks: projectSubagentTasks(store.Snapshot(), viewingTaskID), ViewingTaskID: viewingTaskID}
		r.subagentRevision++
		snapshot.Revision = r.subagentRevision
		r.mu.Unlock()
		return tui.SubagentSnapshotMsg{Snapshot: snapshot}
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

func projectSubagentTasks(states []subagent.TaskState, viewingTaskID string) []tui.SubagentTaskView {
	tasks := make([]tui.SubagentTaskView, 0, len(states))
	for _, state := range states {
		if state == nil || state.Base() == nil {
			continue
		}
		base := state.Base()
		view := tui.SubagentTaskView{
			ID: base.ID, Type: string(base.Type), Status: string(base.Status),
			Description: base.Description, OutputFile: base.OutputFile,
			StartTime: base.StartTime, EndTime: base.EndTime,
			LifecycleToken: base.LifecycleToken, Retain: false,
		}
		switch task := state.(type) {
		case *subagent.AgentTaskState:
			view.AgentType, view.Prompt, view.WorktreePath = task.AgentType, task.Prompt, task.WorktreePath
			view.PendingCount = len(task.PendingMessages)
			view.Revision = subagentTaskRevision(base, len(task.Messages), view.PendingCount)
			if task.ID == viewingTaskID {
				view.Messages = projectSubagentMessages(task.ID, task.Messages)
			}
			if task.Result != nil {
				view.TotalTokens, view.ToolUses = task.Result.TotalTokens, task.Result.TotalToolUseCount
				view.Result = textBlocks(task.Result.Content)
			}
			if task.Progress != nil {
				view.TotalTokens = max(view.TotalTokens, task.Progress.TokenCount)
				view.ToolUses = max(view.ToolUses, task.Progress.ToolUseCount)
				if task.Progress.LastActivity != nil {
					view.LastActivity = task.Progress.LastActivity.ActivityDescription
				}
			}
			view.Error = task.Error
		case *subagent.BashTaskState:
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

func subagentTaskRevision(base *subagent.TaskStateBase, messageCount, pendingCount int) uint64 {
	revision := base.LifecycleToken + 1
	for _, value := range string(base.Status) {
		revision = revision*33 + uint64(value)
	}
	revision = revision*33 + uint64(messageCount)
	revision = revision*33 + uint64(pendingCount)
	return revision
}

func textBlocks(blocks []subagent.TextBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func projectSubagentMessages(taskID string, messages []llm.Message) []tui.Message {
	results := make(map[string]llm.ToolResultMessage)
	for _, message := range messages {
		if result, ok := message.(llm.ToolResultMessage); ok {
			results[result.ToolCallID] = result
		}
	}
	projected := make([]tui.Message, 0, len(messages))
	for index, message := range messages {
		id := fmt.Sprintf("%s/entry/%d", taskID, index)
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
					call := tui.ToolCallView{ID: content.ID, Name: content.Name, Arguments: encodeTaskArguments(content.Arguments), Status: tui.ToolSuccess}
					if result, ok := results[content.ID]; ok {
						call.Result = formatTaskToolResult(result)
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
