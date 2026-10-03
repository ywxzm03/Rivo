package compaction

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// ToolResultCharLimit 序列化时工具结果文本的截断上限
const ToolResultCharLimit = 2000

// SerializeMessages 把消息压平为带角色标签的文本供摘要模型阅读，避免模型把它当成仍在进行的对话；
// thinking 单独标注，工具调用渲染为 name(JSON 参数)，工具结果超长时截断
func SerializeMessages(messages []llm.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		switch m := msg.(type) {
		case llm.UserMessage:
			writeLabeled(&b, "User", contentsText(m.Content))
		case llm.AssistantMessage:
			var thinking, text, calls []string
			for _, c := range m.Content {
				switch c := c.(type) {
				case llm.ThinkingContent:
					if c.Thinking != "" {
						thinking = append(thinking, c.Thinking)
					}
				case llm.TextContent:
					if c.Text != "" {
						text = append(text, c.Text)
					}
				case llm.ToolCall:
					data, _ := json.Marshal(c.Arguments)
					calls = append(calls, c.Name+"("+string(data)+")")
				}
			}
			if len(thinking) > 0 {
				writeLabeled(&b, "Assistant thinking", strings.Join(thinking, "\n"))
			}
			if len(text) > 0 {
				writeLabeled(&b, "Assistant", strings.Join(text, "\n"))
			}
			if len(calls) > 0 {
				writeLabeled(&b, "Assistant tool calls", strings.Join(calls, "; "))
			}
		case llm.ToolResultMessage:
			writeLabeled(&b, "Tool result", m.ToolName+": "+truncateRunes(contentsText(m.Content), ToolResultCharLimit))
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// contentsText 拼接内容块中的文本，图片以占位符标注
func contentsText(contents []llm.Content) string {
	var lines []string
	for _, c := range contents {
		switch c := c.(type) {
		case llm.TextContent:
			if c.Text != "" {
				lines = append(lines, c.Text)
			}
		case llm.ImageContent:
			lines = append(lines, "[image: "+c.MIMEType+"]")
		}
	}
	return strings.Join(lines, "\n")
}

// writeLabeled 写入一行带标签的序列化文本
func writeLabeled(b *strings.Builder, label, text string) {
	if text == "" {
		return
	}
	fmt.Fprintf(b, "[%s]: %s\n", label, text)
}

// truncateRunes 按字符数截断文本并标注省略长度
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + fmt.Sprintf(" [... %d more characters truncated]", len(runes)-limit)
}
