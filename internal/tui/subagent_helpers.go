package tui

// hasRunningSubagent 判断任务列表里是否还有运行中的子任务
func hasRunningSubagent(tasks []SubagentTaskView) bool {
	for _, task := range tasks {
		if task.Status == "running" {
			return true
		}
	}
	return false
}

// cloneMessage 深拷贝单条消息的指针与切片字段
func cloneMessage(message Message) Message {
	copy := message
	copy.ToolGroup = cloneToolGroup(message.ToolGroup)
	if message.Skill != nil {
		skill := *message.Skill
		copy.Skill = &skill
	}
	if message.Divider != nil {
		divider := *message.Divider
		copy.Divider = &divider
	}
	copy.Images = append([]ImageView(nil), message.Images...)
	return copy
}

// cloneMessages 深拷贝消息切片,隔离 ToolGroup 等指针字段
func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	result := make([]Message, len(messages))
	for index, message := range messages {
		result[index] = cloneMessage(message)
	}
	return result
}

// cloneToolGroup 深拷贝工具组,避免快照与实时状态共享底层切片
func cloneToolGroup(group *ToolGroupView) *ToolGroupView {
	if group == nil {
		return nil
	}
	cloned := *group
	cloned.Calls = append([]ToolCallView(nil), group.Calls...)
	cloned.Thinking = append([]ThinkingBlockView(nil), group.Thinking...)
	return &cloned
}

// upsertMainMessage 更新子任务视图期间缓存的主 transcript
func (m *Model) upsertMainMessage(message Message) {
	for index := range m.panel.mainMessages {
		if m.panel.mainMessages[index].ID == message.ID {
			m.panel.mainMessages[index] = message
			return
		}
	}
	m.panel.mainMessages = append(m.panel.mainMessages, message)
}

func (m *Model) replaceMainMessage(previousID string, message Message) {
	for index := range m.panel.mainMessages {
		if m.panel.mainMessages[index].ID == previousID {
			m.panel.mainMessages[index] = message
			return
		}
	}
	m.upsertMainMessage(message)
}

func (m *Model) replaceMainTail(tail MessageTail) {
	start := 0
	if tail.AfterID != "" {
		start = -1
		for index := range m.panel.mainMessages {
			if m.panel.mainMessages[index].ID == tail.AfterID {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return
		}
	}
	m.panel.mainMessages = append(m.panel.mainMessages[:start:start], cloneMessages(tail.Messages)...)
}
