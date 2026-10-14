package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	style "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// markdownRenderer 按当前宽度复用解析器，已完成消息的输出缓存在 Model 中
type markdownRenderer struct {
	width     int
	renderers map[markdownRendererKey]*glamour.TermRenderer
	widths    []int
}

type markdownRendererKey struct {
	width int
	role  string
}

func primitive(color string) style.StylePrimitive { return style.StylePrimitive{Color: &color} }

// markdownStyle 逐项映射 Pi Markdown/语法高亮 token，不混入 Glamour 默认配色
func markdownStyle(role string) style.StyleConfig {
	zero, one := uint(0), uint(1)
	yes := true
	quote, center, column, row := "│ ", "┼", "│", "─"
	quoteColor, bulletColor := mutedColor, accentColor
	text := primitive(textColor)
	if role == "thinking" {
		text = primitive(thinkingColor)
		text.Italic = &yes
	}
	if role == "tool" {
		text = primitive(mutedColor)
	}
	if role == "user" {
		bg := userBg
		text.BackgroundColor = &bg
	}
	heading := primitive(yellowColor)
	heading.Bold, heading.BlockSuffix = &yes, "\n"
	return style.StyleConfig{
		Document:       style.StyleBlock{StylePrimitive: text, Margin: &zero},
		Heading:        style.StyleBlock{StylePrimitive: heading},
		BlockQuote:     style.StyleBlock{StylePrimitive: primitive(mutedColor), Indent: &one, IndentToken: &quote},
		List:           style.StyleList{LevelIndent: 2},
		Emph:           style.StylePrimitive{Italic: &yes},
		Strong:         style.StylePrimitive{Bold: &yes},
		Strikethrough:  style.StylePrimitive{CrossedOut: &yes},
		HorizontalRule: style.StylePrimitive{Color: &quoteColor, Format: "────────"},
		Item:           style.StylePrimitive{Color: &bulletColor, BlockPrefix: "• "},
		Enumeration:    style.StylePrimitive{Color: &bulletColor, BlockPrefix: ". "},
		Task:           style.StyleTask{Ticked: "[✓] ", Unticked: "[ ] "},
		LinkText:       primitive(blueColor), Link: primitive(mutedColor),
		ImageText: style.StylePrimitive{Color: &quoteColor, Format: "[image: {{.text}}]"},
		Image:     primitive(mutedColor),
		Code:      style.StyleBlock{StylePrimitive: primitive(accentColor)},
		CodeBlock: style.StyleCodeBlock{StyleBlock: style.StyleBlock{StylePrimitive: primitive(greenColor), Margin: &zero},
			Chroma: &style.Chroma{
				Text: primitive(greenColor), Error: primitive(redColor),
				Comment: primitive(mutedColor), CommentPreproc: primitive(mutedColor),
				Keyword: primitive(blueColor), KeywordReserved: primitive(blueColor), KeywordNamespace: primitive(blueColor), KeywordType: primitive(accentColor),
				Operator: primitive(mutedColor), Punctuation: primitive(mutedColor),
				Name: primitive(variableColor), NameBuiltin: primitive(yellowColor), NameFunction: primitive(yellowColor), NameClass: primitive(accentColor),
				LiteralString: primitive(stringColor), LiteralStringEscape: primitive(stringColor), LiteralNumber: primitive(greenColor),
				GenericDeleted: primitive(redColor), GenericInserted: primitive(greenColor),
			}},
		Table:                 style.StyleTable{CenterSeparator: &center, ColumnSeparator: &column, RowSeparator: &row},
		DefinitionTerm:        style.StylePrimitive{Bold: &yes},
		DefinitionDescription: style.StylePrimitive{BlockPrefix: "  "},
	}
}

func (r *markdownRenderer) render(text, role string, width int) string {
	width = max(1, width)
	if r.renderers == nil {
		r.renderers = make(map[markdownRendererKey]*glamour.TermRenderer)
	}
	if !containsInt(r.widths, width) {
		r.widths = append(r.widths, width)
		if len(r.widths) > 4 {
			stale := r.widths[0]
			r.widths = r.widths[1:]
			for key := range r.renderers {
				if key.width == stale {
					delete(r.renderers, key)
				}
			}
		}
	}
	r.width = width
	key := markdownRendererKey{width: width, role: role}
	renderer := r.renderers[key]
	if renderer == nil {
		var err error
		renderer, err = glamour.NewTermRenderer(glamour.WithStyles(markdownStyle(role)), glamour.WithWordWrap(width), glamour.WithColorProfile(lipgloss.ColorProfile()))
		if err != nil {
			return colored(redColor, "Markdown: "+err.Error())
		}
		r.renderers[key] = renderer
	}
	result, err := renderer.Render(text)
	if err != nil {
		return colored(redColor, "Markdown: "+err.Error())
	}
	// 代码块也必须适配终端列宽，不能任由下层终端自动换行破坏高度分配
	return ansi.Hardwrap(strings.Trim(result, "\n"), width, true)
}

func containsInt(values []int, value int) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func (m *Model) renderMessage(message Message) string {
	width := max(1, m.width-2)
	if message.Role == "notice" || message.Role == "summary" {
		return colored(mutedColor, ansi.Wrap(message.Text, width, ""))
	}
	if message.Role == "divider" && message.Divider != nil {
		return rule(m.width, "worked for "+formatElapsed(message.Divider.Elapsed), mutedColor, true)
	}
	if message.ToolGroup != nil {
		parts := []string{m.renderToolGroup(message.ToolGroup, width)}
		if message.Text != "" {
			parts = append(parts, indent(m.markdown.render(message.Text, "assistant", width)))
		}
		if message.Aborted {
			// 取消属于用户操作：正文后追加固定红色提示（对齐 Pi 的 aborted 渲染）
			parts = append(parts, colored(redColor, "Operation aborted"))
			return strings.Join(parts, "\n\n")
		}
		if message.Divider != nil && message.Text != "" {
			parts = append(parts, rule(m.width, "worked for "+formatElapsed(message.Divider.Elapsed), mutedColor, true))
		}
		return strings.Join(parts, "\n\n")
	}
	if message.Aborted {
		// 取消属于用户操作：partial 正文后追加固定红色提示（对齐 Pi 的 aborted 渲染）
		parts := []string{}
		if trimmed := strings.TrimSpace(message.Text); trimmed != "" {
			parts = append(parts, m.markdown.render(trimmed, "assistant", width))
		}
		parts = append(parts, colored(redColor, "Operation aborted"))
		return strings.Join(parts, "\n")
	}
	if message.Role == "user" {
		// 用户行使用整行灰色框；去掉 Markdown 的内部 ANSI，避免重置码截断背景
		body := ansi.Strip(m.markdown.render(message.Text, "user", width))
		lines := strings.Split(body, "\n")
		prefixStyle := lipgloss.NewStyle().Background(lipgloss.Color(userBg)).Foreground(lipgloss.Color(mutedColor))
		textStyle := lipgloss.NewStyle().Background(lipgloss.Color(userBg)).Foreground(lipgloss.Color(textColor))
		for i, line := range lines {
			// 仅首行显示箭头，换行后的正文保持相同缩进
			prefix := "  "
			if i == 0 {
				prefix = "❯ "
			}
			padding := max(0, m.width-ansi.StringWidth(prefix+line))
			lines[i] = prefixStyle.Render(prefix) + textStyle.Render(line+strings.Repeat(" ", padding))
		}
		return strings.Join(lines, "\n")
	}
	var parts []string
	if message.Thinking != "" {
		thinking := message.Thinking
		if m.hideThinking {
			thinking = "Thinking..."
		}
		parts = append(parts, indent(m.markdown.render(thinking, "thinking", width)))
	}
	if message.Divider != nil && message.Text != "" {
		parts = append(parts, rule(m.width, "worked for "+formatElapsed(message.Divider.Elapsed), mutedColor, true))
	}
	if message.Text != "" {
		parts = append(parts, indent(m.markdown.render(message.Text, "assistant", width)))
	}
	return strings.Join(parts, "\n\n")
}

func indent(text string) string { return " " + strings.ReplaceAll(text, "\n", "\n ") }
