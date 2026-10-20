package llm

import "strings"

// SystemSectionOrder 是分节渲染顺序的唯一来源，声明为数组以表达不可变
var SystemSectionOrder = [...]string{"preamble", "tools", "rules", "instructions", "skills", "cwd"}

func SystemSectionNames() []string {
	names := make([]string, len(SystemSectionOrder))
	copy(names, SystemSectionOrder[:])
	return names
}

// CurrentSystemMessage 按消息序重放所有 system 消息：同名分节后者覆盖前者，
// 空串表示删除；没有 system 消息时返回 nil
func CurrentSystemMessage(messages []Message) *SystemMessage {
	sections := make(map[string]string)
	found := false
	for _, msg := range messages {
		m, ok := msg.(SystemMessage)
		if !ok {
			continue
		}
		found = true
		for name, value := range m.Sections {
			if value == "" {
				delete(sections, name)
				continue
			}
			sections[name] = value
		}
	}
	if !found {
		return nil
	}
	return &SystemMessage{Sections: sections}
}

// RenderSystemMessageText 按 SystemSectionOrder 拼接各节文本，供首条 system 消息完整渲染
func RenderSystemMessageText(msg SystemMessage) string {
	var parts []string
	for _, name := range SystemSectionOrder {
		if text := msg.Sections[name]; text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// RenderSystemMessageUpdate 把分节变更渲染为补丁文本，空串值渲染为删除文案
func RenderSystemMessageUpdate(msg SystemMessage) string {
	var parts []string
	for _, name := range SystemSectionOrder {
		value, ok := msg.Sections[name]
		if !ok {
			continue
		}
		if value == "" {
			parts = append(parts, "Removed system prompt section \""+name+"\".")
			continue
		}
		parts = append(parts, "Updated system prompt section \""+name+"\":\n\n"+value)
	}
	return strings.Join(parts, "\n\n")
}
