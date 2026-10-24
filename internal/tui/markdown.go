package tui

import (
	"strings"
	"time"

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

func markdownStyle(role string) style.StyleConfig {
	primitive := func(color string) style.StylePrimitive {
		if role == "skill" {
			color = textColor
		}
		return style.StylePrimitive{Color: &color}
	}
	zero, one := uint(0), uint(1)
	yes := true
	quote, center, column, row := "│ ", "┼", "│", "─"
	quoteColor, bulletColor := mutedColor, accentColor
	if role == "skill" {
		quoteColor, bulletColor = textColor, textColor
	}
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
	heading := primitive(textColor)
	heading.BlockSuffix = "\n"
	h1 := style.StyleBlock{StylePrimitive: heading}
	h1.Bold, h1.Italic, h1.Underline = &yes, &yes, &yes
	h2 := style.StyleBlock{StylePrimitive: heading}
	h2.Bold = &yes
	h3 := style.StyleBlock{StylePrimitive: heading}
	h3.Bold = &yes
	quoteStyle := primitive(mutedColor)
	quoteStyle.Italic = &yes
	codeText := primitive(codeTextColor)
	return style.StyleConfig{
		Document:       style.StyleBlock{StylePrimitive: text, Margin: &zero},
		Heading:        style.StyleBlock{StylePrimitive: heading},
		H1:             h1,
		H2:             h2,
		H3:             h3,
		H4:             h3,
		H5:             h3,
		H6:             h3,
		BlockQuote:     style.StyleBlock{StylePrimitive: quoteStyle, Indent: &one, IndentToken: &quote},
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
		CodeBlock: style.StyleCodeBlock{StyleBlock: style.StyleBlock{StylePrimitive: codeText, Margin: &zero},
			Chroma: &style.Chroma{
				Text: primitive(codeTextColor), Error: primitive(redColor),
				Comment: primitive(syntaxCommentColor), CommentPreproc: primitive(syntaxCommentColor),
				Keyword: primitive(syntaxKeywordColor), KeywordReserved: primitive(syntaxKeywordColor), KeywordNamespace: primitive(syntaxStorageColor), KeywordType: primitive(syntaxTypeColor),
				Operator: primitive(syntaxKeywordColor), Punctuation: primitive(codeTextColor),
				Name: primitive(variableColor), NameBuiltin: primitive(syntaxTypeColor), NameFunction: primitive(syntaxTypeColor), NameClass: primitive(syntaxTypeColor),
				NameTag: primitive(syntaxTypeColor), NameAttribute: primitive(syntaxTypeColor), NameConstant: primitive(syntaxLiteralColor), NameDecorator: primitive(syntaxTypeColor), NameException: primitive(redColor), NameOther: primitive(variableColor),
				Literal: primitive(syntaxLiteralColor), LiteralString: primitive(stringColor), LiteralStringEscape: primitive(stringColor), LiteralNumber: primitive(syntaxLiteralColor), LiteralDate: primitive(syntaxLiteralColor),
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
	// 代码块也要适配终端列宽，避免下层终端自动换行破坏高度分配
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
	if message.Role == "context" {
		return colored(textColor, ansi.Wrap(message.Text, width, ""))
	}
	if message.Role == "notice" || message.Role == "summary" {
		return colored(mutedColor, ansi.Wrap(message.Text, width, ""))
	}
	if message.Role == "divider" && message.Divider != nil {
		return dividerLine(m.width, message.Divider.Elapsed)
	}
	if message.ToolGroup != nil {
		group := m.renderToolGroup(message.ToolGroup, width)
		m.lastMessageHits = append([]toolHit(nil), m.lastToolHits...)
		parts := []string{group}
		if message.Text != "" {
			parts = append(parts, indent(m.markdown.render(message.Text, "assistant", width)))
		}
		if message.Aborted {
			// 取消是用户操作，正文后追加固定红色提示
			parts = append(parts, colored(redColor, "Operation aborted"))
			return strings.Join(parts, "\n\n")
		}
		if message.Divider != nil && message.Text != "" {
			parts = append(parts, dividerLine(m.width, message.Divider.Elapsed))
		}
		return strings.Join(parts, "\n\n")
	}
	if message.Aborted {
		// 取消是用户操作，partial 正文后追加固定红色提示
		parts := []string{}
		if trimmed := strings.TrimSpace(message.Text); trimmed != "" {
			parts = append(parts, m.markdown.render(trimmed, "assistant", width))
		}
		parts = append(parts, colored(redColor, "Operation aborted"))
		return strings.Join(parts, "\n")
	}
	if message.Role == "user" {
		if skill := message.Skill; skill != nil {
			label := "[skill] " + skill.Name
			var parts []string
			if m.showAllTools {
				parts = append(parts, colored(accentColor, ansi.Wrap(label, width, "")), indent(m.markdown.render(skill.Content, "skill", width)))
			} else {
				parts = append(parts, colored(mutedColor, ansi.Wrap(label+" (Ctrl+O to expand)", width, "")))
			}
			if skill.Arguments != "" {
				parts = append(parts, m.renderMessage(Message{Role: "user", Text: skill.Arguments}))
			}
			return strings.Join(parts, "\n\n")
		}
		// 用户行使用整行灰色框，去掉 Markdown 的内部 ANSI 避免重置码截断背景
		bodyText := message.Text
		for _, image := range message.Images {
			label := "[image]"
			if image.MIMEType != "" {
				label = "[image: " + image.MIMEType + "]"
			}
			if bodyText != "" {
				bodyText += "\n"
			}
			bodyText += label
		}
		body := ansi.Strip(m.markdown.render(bodyText, "user", width))
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
		if !m.showThinking {
			parts = append(parts, boldItalicText("Thinking..."))
		} else {
			parts = append(parts, indent(m.markdown.render(message.Thinking, "thinking", width)))
		}
	}
	if message.Text != "" {
		parts = append(parts, indent(m.markdown.render(message.Text, "assistant", width)))
	}
	if message.Divider != nil && message.Text != "" {
		parts = append(parts, dividerLine(m.width, message.Divider.Elapsed))
	}
	return strings.Join(parts, "\n\n")
}

// dividerLine 输出回合结束行：耗时，纯文字不带分隔线
func dividerLine(width int, elapsed time.Duration) string {
	return colored(mutedColor, ansi.Truncate(indent("Worked for "+formatElapsed(elapsed)), width, "…"))
}

func indent(text string) string { return " " + strings.ReplaceAll(text, "\n", "\n ") }
